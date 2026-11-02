package main

import (
	"context"
	"net/http"
)

func (a *API) githubConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Data: nil, Error: "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, APIResponse{Success: true, Data: map[string]bool{"enabled": a.GitHub.enabled()}, Error: nil})
}

func (a *API) githubDeviceStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Data: nil, Error: "method not allowed"})
		return
	}
	if !a.GitHub.enabled() || a.Sessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, APIResponse{Success: false, Data: nil, Error: "GitHub App is not configured on the agent"})
		return
	}
	sessionID, ok := a.sessionID(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, APIResponse{Success: false, Data: nil, Error: "login session is required"})
		return
	}
	result, err := a.GitHub.startDeviceFlow(r.Context(), sessionID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, APIResponse{Success: false, Data: nil, Error: err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, APIResponse{Success: true, Data: result, Error: nil})
}

func (a *API) githubDevicePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Data: nil, Error: "method not allowed"})
		return
	}
	if !a.GitHub.enabled() || a.Sessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, APIResponse{Success: false, Data: nil, Error: "GitHub App is not configured on the agent"})
		return
	}
	sessionID, ok := a.sessionID(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, APIResponse{Success: false, Data: nil, Error: "login session is required"})
		return
	}
	result, err := a.GitHub.pollDeviceFlow(r.Context(), sessionID, a.Sessions)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, APIResponse{Success: false, Data: nil, Error: err.Error()})
		return
	}
	status := http.StatusOK
	if result["status"] == "pending" {
		status = http.StatusAccepted
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, APIResponse{Success: true, Data: result, Error: nil})
}

func (a *API) githubRepositories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Data: nil, Error: "method not allowed"})
		return
	}
	sessionID, ok := a.sessionID(r)
	if !ok || a.GitHub == nil || a.Sessions == nil {
		writeJSON(w, http.StatusUnauthorized, APIResponse{Success: false, Data: nil, Error: "connect a GitHub account first"})
		return
	}
	repositories, err := a.GitHub.repositories(r.Context(), a.Sessions, sessionID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, APIResponse{Success: false, Data: nil, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, APIResponse{Success: true, Data: repositories, Error: nil})
}

func (a *API) githubBranches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Data: nil, Error: "method not allowed"})
		return
	}
	sessionID, ok := a.sessionID(r)
	if !ok || a.GitHub == nil || a.Sessions == nil {
		writeJSON(w, http.StatusUnauthorized, APIResponse{Success: false, Data: nil, Error: "connect a GitHub account first"})
		return
	}
	branches, err := a.GitHub.branches(r.Context(), a.Sessions, sessionID, r.URL.Query().Get("repository"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, APIResponse{Success: false, Data: nil, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, APIResponse{Success: true, Data: branches, Error: nil})
}

func (a *API) githubDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Data: nil, Error: "method not allowed"})
		return
	}
	sessionID, ok := a.sessionID(r)
	if ok && a.Sessions != nil {
		a.Sessions.clearGitHub(sessionID)
	}
	if ok && a.GitHub != nil {
		a.GitHub.removePending(sessionID)
	}
	writeJSON(w, http.StatusOK, APIResponse{Success: true, Data: nil, Error: nil})
}

func (a *API) sessionID(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || a.Sessions == nil || !a.Sessions.valid(cookie.Value) {
		return "", false
	}
	return cookie.Value, true
}

func (a *API) githubAccessToken(ctx context.Context, r *http.Request) (string, bool, error) {
	sessionID, ok := a.sessionID(r)
	if !ok || a.GitHub == nil || a.Sessions == nil {
		return "", false, nil
	}
	return a.GitHub.accessToken(ctx, a.Sessions, sessionID)
}
