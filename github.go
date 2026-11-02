package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	githubDeviceURL = "https://github.com/login/device/code"
	githubTokenURL  = "https://github.com/login/oauth/access_token"
	githubAPIURL    = "https://api.github.com"
)

type githubClient struct {
	clientID string
	client   *http.Client
	mu       sync.Mutex
	pending  map[string]githubDeviceAuthorization
}

type githubDeviceAuthorization struct {
	deviceCode string
	interval   time.Duration
	expires    time.Time
}

type githubDeviceStart struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

type githubTokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_token_expires_in"`
	Error            string `json:"error"`
}

type githubRepository struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

func newGitHubClient(clientID string) *githubClient {
	return &githubClient{
		clientID: clientID,
		client:   &http.Client{Timeout: 15 * time.Second},
		pending:  make(map[string]githubDeviceAuthorization),
	}
}

func (g *githubClient) enabled() bool { return g != nil && g.clientID != "" }

func (g *githubClient) startDeviceFlow(ctx context.Context, sessionID string) (map[string]any, error) {
	values := url.Values{"client_id": {g.clientID}, "scope": {"offline_access"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubDeviceURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach GitHub")
	}
	defer res.Body.Close()
	var data githubDeviceStart
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&data) != nil {
		return nil, fmt.Errorf("GitHub could not start device authorization")
	}
	if data.DeviceCode == "" || data.UserCode == "" || data.VerificationURL != "https://github.com/login/device" || data.ExpiresIn <= 0 || data.Interval <= 0 {
		return nil, fmt.Errorf("GitHub returned an invalid device authorization")
	}
	g.mu.Lock()
	for session, authorization := range g.pending {
		if time.Now().After(authorization.expires) {
			delete(g.pending, session)
		}
	}
	g.pending[sessionID] = githubDeviceAuthorization{deviceCode: data.DeviceCode, interval: time.Duration(data.Interval) * time.Second, expires: time.Now().Add(time.Duration(data.ExpiresIn) * time.Second)}
	g.mu.Unlock()
	return map[string]any{
		"user_code": data.UserCode, "verification_uri": data.VerificationURL,
		"interval": data.Interval, "expires_in": data.ExpiresIn,
	}, nil
}

func (g *githubClient) pollDeviceFlow(ctx context.Context, sessionID string, sessions *sessionManager) (map[string]any, error) {
	g.mu.Lock()
	pending, ok := g.pending[sessionID]
	g.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("start GitHub connection first")
	}
	if time.Now().After(pending.expires) {
		g.removePending(sessionID)
		return nil, fmt.Errorf("GitHub authorization code expired; start again")
	}
	values := url.Values{
		"client_id":   {g.clientID},
		"device_code": {pending.deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach GitHub")
	}
	defer res.Body.Close()
	var token githubTokenResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&token); err != nil {
		return nil, fmt.Errorf("GitHub returned an invalid authorization response")
	}
	switch token.Error {
	case "authorization_pending":
		return map[string]any{"status": "pending", "interval": int(pending.interval.Seconds())}, nil
	case "slow_down":
		pending.interval += 5 * time.Second
		g.mu.Lock()
		g.pending[sessionID] = pending
		g.mu.Unlock()
		return map[string]any{"status": "pending", "interval": int(pending.interval.Seconds())}, nil
	case "expired_token":
		g.removePending(sessionID)
		return nil, fmt.Errorf("GitHub authorization code expired; start again")
	case "access_denied":
		g.removePending(sessionID)
		return nil, fmt.Errorf("GitHub authorization was cancelled")
	case "":
		if res.StatusCode != http.StatusOK || token.AccessToken == "" {
			return nil, fmt.Errorf("GitHub could not authorize this account")
		}
	default:
		g.removePending(sessionID)
		return nil, fmt.Errorf("GitHub authorization failed; start again")
	}
	user, err := g.getUser(ctx, token.AccessToken)
	if err != nil {
		return nil, err
	}
	sessions.setGitHub(sessionID, githubAuth{
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		ExpiresAt:        time.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
		RefreshExpiresAt: time.Now().Add(time.Duration(token.RefreshExpiresIn) * time.Second),
		Username:         user.Login,
	})
	g.removePending(sessionID)
	return map[string]any{"status": "connected", "username": user.Login}, nil
}

func (g *githubClient) removePending(sessionID string) {
	g.mu.Lock()
	delete(g.pending, sessionID)
	g.mu.Unlock()
}

func (g *githubClient) accessToken(ctx context.Context, sessions *sessionManager, sessionID string) (string, bool, error) {
	auth, ok := sessions.githubAuth(sessionID)
	if !ok {
		return "", false, nil
	}
	if time.Now().Add(time.Minute).Before(auth.ExpiresAt) {
		return auth.AccessToken, true, nil
	}
	if auth.RefreshToken == "" || time.Now().After(auth.RefreshExpiresAt) {
		sessions.clearGitHub(sessionID)
		return "", true, fmt.Errorf("GitHub connection expired; reconnect or enter a token manually")
	}
	values := url.Values{
		"client_id":     {g.clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {auth.RefreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return "", true, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := g.client.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("could not refresh GitHub connection")
	}
	defer res.Body.Close()
	var refreshed githubTokenResponse
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&refreshed) != nil || refreshed.AccessToken == "" {
		sessions.clearGitHub(sessionID)
		return "", true, fmt.Errorf("GitHub connection expired; reconnect or enter a token manually")
	}
	auth.AccessToken = refreshed.AccessToken
	if refreshed.RefreshToken != "" {
		auth.RefreshToken = refreshed.RefreshToken
	}
	auth.ExpiresAt = time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
	if refreshed.RefreshExpiresIn > 0 {
		auth.RefreshExpiresAt = time.Now().Add(time.Duration(refreshed.RefreshExpiresIn) * time.Second)
	}
	sessions.setGitHub(sessionID, auth)
	return auth.AccessToken, true, nil
}

func (g *githubClient) getUser(ctx context.Context, token string) (struct {
	Login string `json:"login"`
}, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := g.githubGET(ctx, token, githubAPIURL+"/user", &user); err != nil {
		return user, err
	}
	if user.Login == "" {
		return user, fmt.Errorf("GitHub did not return an account name")
	}
	return user, nil
}

func (g *githubClient) repositories(ctx context.Context, sessions *sessionManager, sessionID string) ([]githubRepository, error) {
	token, connected, err := g.accessToken(ctx, sessions, sessionID)
	if err != nil {
		return nil, err
	}
	if !connected {
		return nil, fmt.Errorf("connect a GitHub account first")
	}
	repositories := make([]githubRepository, 0)
	for page := 1; page <= 10; page++ {
		endpoint := githubAPIURL + "/user/repos?per_page=100&sort=updated&affiliation=owner%2Ccollaborator%2Corganization_member&page=" + strconv.Itoa(page)
		var batch []githubRepository
		if err := g.githubGET(ctx, token, endpoint, &batch); err != nil {
			return nil, err
		}
		repositories = append(repositories, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return repositories, nil
}

func (g *githubClient) branches(ctx context.Context, sessions *sessionManager, sessionID, repository string) ([]string, error) {
	if !githubRepositoryPattern.MatchString(repository) {
		return nil, fmt.Errorf("invalid repository")
	}
	token, connected, err := g.accessToken(ctx, sessions, sessionID)
	if err != nil {
		return nil, err
	}
	if !connected {
		return nil, fmt.Errorf("connect a GitHub account first")
	}
	names := make([]string, 0)
	for page := 1; page <= 10; page++ {
		endpoint := githubAPIURL + "/repos/" + repository + "/branches?per_page=100&page=" + strconv.Itoa(page)
		var batch []struct {
			Name string `json:"name"`
		}
		if err := g.githubGET(ctx, token, endpoint, &batch); err != nil {
			return nil, err
		}
		for _, branch := range batch {
			names = append(names, branch.Name)
		}
		if len(batch) < 100 {
			break
		}
	}
	return names, nil
}

var githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (g *githubClient) githubGET(ctx context.Context, token, endpoint string, output any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "deploy-auto")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach GitHub")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub request failed with status %d", res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(output); err != nil {
		return fmt.Errorf("GitHub returned an invalid response")
	}
	return nil
}
