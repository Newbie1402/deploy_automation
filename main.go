package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	root := envOr("WORKSPACE_ROOT", "./workspaces")
	dataDir := envOr("DATA_DIR", "./data")
	if err := os.MkdirAll(root, 0700); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatal(err)
	}
	jobs := newJobStore()
	deployer := &Deployer{Docker: &CLIDockerManager{}, Git: &GitManager{}, Root: root, BuildTTL: 45 * time.Minute, RunTTL: 2 * time.Minute, Audit: filepath.Join(dataDir, "deployments.jsonl")}
	username, password := os.Getenv("DEPLOY_USERNAME"), os.Getenv("DEPLOY_PASSWORD")
	if (username == "") != (password == "") {
		log.Fatal("DEPLOY_USERNAME and DEPLOY_PASSWORD must both be configured")
	}
	checkCtx, cancelCheck := context.WithTimeout(context.Background(), 10*time.Second)
	if err := deployer.Docker.CheckAvailable(checkCtx); err != nil {
		cancelCheck()
		log.Fatal(err)
	}
	cancelCheck()
	api := &API{Jobs: jobs, Deployer: deployer, Queue: make(chan deployTask, 128)}
	if username != "" {
		api.Sessions = newSessionManager(username, password)
	}
	api.runWorkers(4)
	addr := envOr("API_ADDR", "127.0.0.1:8080")
	if !safeBind(addr) {
		log.Fatal("API_ADDR must be loopback or assigned to a Tailscale/WireGuard interface")
	}
	if !localBind(addr) && os.Getenv("DEPLOY_API_TOKEN") == "" && api.Sessions == nil {
		log.Fatal("DEPLOY_USERNAME and DEPLOY_PASSWORD or DEPLOY_API_TOKEN are required when API_ADDR is not loopback")
	}
	server := &http.Server{Addr: addr, Handler: api.routes(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("deploy agent listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
