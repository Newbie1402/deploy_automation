# deploy-auto

A small Go HTTP agent that syncs a GitHub repository, builds its Docker image, and restarts one named container. Published container ports are bound only to a Tailscale or WireGuard IPv4 address.

## Requirements

- Go 1.23 or newer
- `git` and Docker CLI installed
- The agent user can access the Docker daemon
- The selected VPN interface is up and has an IPv4 address

## Run

```sh
go run .
```

Open the agent address in a browser to use the embedded Deploy Console. Enter the
agent API token in the page, then provide a GitHub repository, branch, ports, and
VPN interface. The console submits deployments and streams job/container logs.
The token is kept in the current browser tab's session storage; use HTTPS or a
trusted VPN connection when accessing the console.

The API listens on `127.0.0.1:8080` by default. Set `WORKSPACE_ROOT` for checked-out repositories, `DATA_DIR` for the audit log, and `API_ADDR` to listen on a VPN address. Any non-loopback `API_ADDR` requires `DEPLOY_API_TOKEN`; clients then send `Authorization: Bearer <token>`.

```sh
WORKSPACE_ROOT=/srv/deploy/workspaces \
DATA_DIR=/var/lib/deploy-agent \
API_ADDR=100.80.1.2:8080 \
DEPLOY_API_TOKEN='replace-with-a-long-random-token' \
go run .
```

Keep the agent API firewall restricted to the VPN. The container's published port is always bound to the discovered VPN IPv4 address; the agent rejects requests if the selected interface or address is unavailable.

## Deploy request

`POST /api/v1/apps/deploy` returns `202 Accepted` with a job ID. `source_path` is relative to `WORKSPACE_ROOT` (defaults to `app_name`).

```json
{
  "manifest": {
    "app_name": "example",
    "source_path": "example",
    "container_port": 8080,
    "host_port": 18080,
    "restart_policy": "unless-stopped",
    "memory_limit": "512m",
    "cpu_limit": "1.5",
    "dockerfile": "FROM node:22-alpine\nWORKDIR /app\nCOPY package*.json ./\nRUN npm ci\nCOPY . .\nRUN npm run build\nFROM nginx:alpine\nCOPY --from=0 /app/dist /usr/share/nginx/html",
    "dockerfile": "FROM node:22-alpine\nWORKDIR /app\nCOPY package*.json ./\nRUN npm ci\nCOPY . .\nRUN npm run build\nFROM nginx:alpine\nCOPY --from=0 /app/dist /usr/share/nginx/html",
    "environment": {
      "APP_MODE": "production",
      "API_KEY": "a value with $pecial characters"
    },
    "healthcheck_path": "/health"
  },
  "git": {
    "repository": "https://github.com/owner/repository.git",
    "branch": "main",
    "token": "github-personal-access-token"
  },
  "network": {
    "type": "tailscale",
    "interface": "tailscale0"
  }
}
```

Private repositories can use an HTTPS PAT or an SSH repository with an absolute `ssh_key_path` already installed on the agent. SSH host verification uses the agent user's `known_hosts` file.

When a repository has no Dockerfile, set `manifest.dockerfile` to its contents. The agent writes this override into the checked-out worktree before building and does not change the upstream repository.

When a repository has no Dockerfile, set `manifest.dockerfile` to its contents. The agent writes that override into the checked-out worktree before building and does not change the upstream repository.

Environment values are written to a short-lived mode-0600 Docker env file and removed after container creation. Values containing newline or NUL characters are rejected because Docker CLI env files cannot represent them safely. Docker stores container environment in its metadata, so limit access to the Docker daemon.

## Job status, events, and logs

- `GET /api/v1/jobs/{job_id}` — current status
- `GET /api/v1/jobs/{job_id}/events` — deploy/build output as SSE
- `GET /api/v1/apps/{app_name}/logs` — container logs as SSE

Audit records are appended to `DATA_DIR/deployments.jsonl`. The service keeps the newest 500 jobs in memory and the latest 2,000 events per job.

## Operational notes

Builds are limited to 45 minutes; container start and optional HTTP health checks are limited to 2 minutes. Four workers process a queue of up to 128 pending deploys.

Run this agent image on a Linux VPS with host networking and the Docker socket mounted so it can see the host VPN interface and control the host daemon. The mounted socket grants root-equivalent access to that daemon.

Example agent start command:

```sh
docker run -d --name deploy-auto --restart unless-stopped --network host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/deploy-agent:/var/lib/deploy-agent \
  -e API_ADDR=100.x.y.z:8080 \
  -e DEPLOY_API_TOKEN='<long-random-token>' \
  deploy-auto:<image-tag>
```

The previous container is left untouched if Git sync or image build fails. Once the new image has built, the old container is stopped and removed to release its fixed host port. If the new container fails to start or pass its optional health check, the previous image and environment are started again. This rollback has a brief interruption because both versions cannot bind the same host IP and port at once.

Old images are retained for rollback and are not pruned automatically.
