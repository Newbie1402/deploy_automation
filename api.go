package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

type API struct {
	Jobs     *jobStore
	Deployer *Deployer
	Queue    chan deployTask
}

type deployTask struct {
	ctx     context.Context
	request DeployRequest
	job     *job
}

func (a *API) runWorkers(count int) {
	for i := 0; i < count; i++ {
		go func() {
			for task := range a.Queue {
				func() {
					defer func() {
						if recover() != nil {
							task.job.update(JobFailed, "deployment worker encountered an internal error")
						}
					}()
					a.Deployer.Deploy(task.ctx, task.request, task.job)
				}()
			}
		}()
	}
}

func (a *API) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/apps/deploy", a.deploy)
	mux.HandleFunc("/api/v1/jobs/", a.jobRoute)
	mux.HandleFunc("/api/v1/apps/", a.logsRoute)
	return a.authenticate(mux)
}

func (a *API) authenticate(next http.Handler) http.Handler {
	token := envOr("DEPLOY_API_TOKEN", "")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, APIResponse{Success: false, Data: nil, Error: "unauthorized"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) deploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, APIResponse{Success: false, Data: nil, Error: "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req DeployRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, APIResponse{Success: false, Data: nil, Error: "invalid request JSON"})
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, APIResponse{Success: false, Data: nil, Error: "request must contain one JSON object"})
		return
	}
	if req.Manifest.AppName == "" || req.Git.Repository == "" || req.Git.Branch == "" {
		writeJSON(w, http.StatusBadRequest, APIResponse{Success: false, Data: nil, Error: "manifest.app_name, git.repository and git.branch are required"})
		return
	}
	j := a.Jobs.create(req.Manifest.AppName, r.RemoteAddr)
	select {
	case a.Queue <- deployTask{ctx: context.WithoutCancel(r.Context()), request: req, job: j}:
	default:
		j.update(JobFailed, "deployment queue is full")
		writeJSON(w, http.StatusServiceUnavailable, APIResponse{Success: false, Data: nil, Error: "deployment queue is full"})
		return
	}
	writeJSON(w, http.StatusAccepted, APIResponse{Success: true, Data: j.view(), Error: nil})
}

func (a *API) jobRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/jobs/")
	parts := strings.Split(rest, "/")
	j := a.Jobs.get(parts[0])
	if j == nil {
		writeJSON(w, http.StatusNotFound, APIResponse{Success: false, Data: nil, Error: "job not found"})
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, APIResponse{Success: true, Data: j.view(), Error: nil})
		return
	}
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
		a.streamJob(w, r, j)
		return
	}
	writeJSON(w, http.StatusNotFound, APIResponse{Success: false, Data: nil, Error: "route not found"})
}

func (a *API) streamJob(w http.ResponseWriter, r *http.Request, j *job) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, APIResponse{Success: false, Data: nil, Error: "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	for {
		j.mu.Lock()
		events := append([]jobEvent(nil), j.events...)
		notify, done := j.notify, j.done
		j.mu.Unlock()
		for _, event := range events {
			if event.ID <= after {
				continue
			}
			payload, _ := json.Marshal(event)
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.ID, payload)
			after = event.ID
		}
		flusher.Flush()
		if done {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-notify:
		}
	}
}

func (a *API) logsRoute(w http.ResponseWriter, r *http.Request) {
	const prefix = "/api/v1/apps/"
	if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/logs") {
		writeJSON(w, http.StatusNotFound, APIResponse{Success: false, Data: nil, Error: "route not found"})
		return
	}
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/logs")
	if !appNamePattern.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, APIResponse{Success: false, Data: nil, Error: "invalid app name"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, APIResponse{Success: false, Data: nil, Error: "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	writer := &sseWriter{w: w, flush: flusher.Flush}
	if err := a.Deployer.Docker.StreamLogs(r.Context(), name, writer); err != nil && r.Context().Err() == nil {
		writer.Write([]byte("docker logs stream ended: " + err.Error() + "\n"))
	}
}

type sseWriter struct {
	mu    sync.Mutex
	w     io.Writer
	flush func()
}

func (s *sseWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(p)
	for _, value := range strings.Split(strings.TrimSuffix(string(p), "\n"), "\n") {
		if _, err := fmt.Fprintf(s.w, "data: %s\n\n", strings.TrimSuffix(value, "\r")); err != nil {
			return 0, err
		}
	}
	s.flush()
	return n, nil
}

func writeJSON(w http.ResponseWriter, status int, value APIResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func localBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func safeBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return true
	}
	if ip == nil || ip.IsUnspecified() {
		return false
	}
	_, tailscale, _ := net.ParseCIDR("100.64.0.0/10")
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var local net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				local = value.IP
			case *net.IPAddr:
				local = value.IP
			}
			if !local.Equal(ip) {
				continue
			}
			isTailscale := (iface.Name == "tailscale0" || strings.HasPrefix(iface.Name, "utun") || strings.Contains(iface.Name, "tailscale")) && tailscale.Contains(ip)
			isWireGuard := strings.HasPrefix(iface.Name, "wg") && (ip.IsPrivate() || tailscale.Contains(ip))
			if isTailscale || isWireGuard {
				return true
			}
		}
	}
	return false
}
