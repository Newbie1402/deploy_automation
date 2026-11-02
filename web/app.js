const $ = (id) => document.getElementById(id);
const jobLog = $("job-log");
const containerLog = $("container-log");
const deployButton = $("deploy-button");
const loginButton = $("login-button");
let jobController;
let appLogController;
let activeStream = "";
let activeJobId = "";
let githubPollTimer;
let githubPollInterval = 5;
let githubRepositories = new Map();
let githubConnected = false;

function apiHeaders(extra = {}) {
  return Object.assign({}, extra);
}

function showLogin(message = "") {
  $("console-view").hidden = true;
  $("session-bar").hidden = true;
  $("login-view").hidden = false;
  $("login-error").textContent = message;
  $("login-error").hidden = !message;
}

function showConsole(username, githubConnected = false, githubUsername = "") {
  $("login-view").hidden = true;
  $("console-view").hidden = false;
  $("session-bar").hidden = !username;
  $("account-name").textContent = username || "";
  loadGitHubConfig(githubConnected, githubUsername);
  restorePreviousJob();
}

async function apiFetch(url, options = {}) {
  const response = await fetch(url, Object.assign({ credentials: "same-origin" }, options));
  if (response.status === 401) {
    showLogin("Phiên đăng nhập đã hết hạn. Vui lòng đăng nhập lại.");
    throw new Error("Phiên đăng nhập đã hết hạn.");
  }
  return response;
}

async function restoreSession() {
  try {
    const response = await fetch("/api/v1/auth/me", { credentials: "same-origin" });
    const result = await response.json();
    if (result.success && result.data.authenticated) showConsole(result.data.username || "", result.data.github_connected, result.data.github_username || "");
    else showLogin();
  } catch {
    showLogin("Không kết nối được agent. Hãy thử tải lại trang.");
  }
}

function appendLog(target, value) {
  const placeholder = target.querySelector(".log-placeholder");
  if (placeholder) placeholder.remove();
  target.textContent += value + "\n";
  const lines = target.textContent.split("\n");
  if (lines.length > 501) target.textContent = lines.slice(-500).join("\n") + "\n";
  target.scrollTop = target.scrollHeight;
}

function setStatus(status, message = "") {
  const badge = $("job-status");
  const labels = {
    PENDING: "ĐANG CHỜ", CLONING: "ĐỒNG BỘ", BUILDING: "ĐANG BUILD",
    RUNNING: "ĐANG CHẠY", SUCCESS: "THÀNH CÔNG", FAILED: "THẤT BẠI",
    ROLLED_BACK: "ĐÃ ROLLBACK"
  };
  badge.textContent = labels[status] || status;
  badge.className = "status-chip";
  if (["PENDING", "CLONING", "BUILDING", "RUNNING"].includes(status)) badge.classList.add("running");
  if (status === "SUCCESS" || status === "ROLLED_BACK") badge.classList.add("success");
  if (status === "FAILED") badge.classList.add("failed");
  if (message) $("job-meta").firstElementChild.textContent = message;
}

function parseEnvironment(value) {
  const output = {};
  for (const [index, line] of value.split(/\r?\n/).entries()) {
    if (!line.trim() || line.trimStart().startsWith("#")) continue;
    const splitAt = line.indexOf("=");
    if (splitAt < 1) throw new Error("Dòng biến môi trường " + (index + 1) + " cần theo dạng KEY=VALUE.");
    const key = line.slice(0, splitAt).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) throw new Error("Tên biến môi trường không hợp lệ ở dòng " + (index + 1) + ".");
    output[key] = line.slice(splitAt + 1);
  }
  return output;
}

async function loadGitHubConfig(connected, githubUsername = "") {
  githubConnected = connected;
  try {
    const response = await apiFetch("/api/v1/github/config");
    const result = await response.json();
    $("github-connect-panel").hidden = false;
    if (!result.success || !result.data.enabled) {
      $("github-connect-title").textContent = "Kết nối GitHub để chọn repository";
      $("github-connect-message").textContent = "Agent chưa được cấu hình GitHub App. Bạn vẫn có thể nhập URL và PAT thủ công.";
      $("github-connect-button").disabled = true;
      $("github-connect-button").textContent = "Chưa cấu hình";
      return;
    }
    $("github-connect-button").disabled = false;
    if (connected) {
      showGitHubConnected(githubUsername);
      await loadGitHubRepositories();
    } else {
      $("github-connect-title").textContent = "Chọn repository trực tiếp từ GitHub";
      $("github-connect-message").textContent = "Kết nối tài khoản; token được giữ phía agent.";
      $("github-connect-button").textContent = "Kết nối GitHub";
    }
  } catch {
    $("github-connect-panel").hidden = false;
    $("github-connect-message").textContent = "Không tải được cấu hình GitHub. Bạn vẫn có thể nhập URL và PAT thủ công.";
  }
}

function showGitHubConnected(username) {
  githubConnected = true;
  $("github-connect-title").textContent = "Đã kết nối GitHub" + (username ? " · @" + username : "");
  $("github-connect-message").textContent = "Agent sẽ dùng quyền GitHub này khi bạn triển khai. PAT thủ công vẫn có thể ghi đè.";
  $("github-connect-button").textContent = "Ngắt khỏi agent";
  $("github-device").hidden = true;
  $("github-repository-field").hidden = false;
}

async function loadGitHubRepositories() {
  const response = await apiFetch("/api/v1/github/repos");
  const result = await response.json();
  if (!response.ok || !result.success) throw new Error(result.error || "Không tải được repository GitHub.");
  const select = $("github-repository");
  select.replaceChildren(new Option("Chọn repository", ""));
  githubRepositories = new Map();
  for (const repo of result.data) {
    githubRepositories.set(repo.full_name, repo);
    select.add(new Option(repo.full_name + (repo.private ? " · riêng tư" : ""), repo.full_name));
  }
}

async function loadGitHubBranches(repository) {
  const branchField = $("github-branch-field");
  branchField.hidden = true;
  const response = await apiFetch("/api/v1/github/branches?repository=" + encodeURIComponent(repository));
  const result = await response.json();
  if (!response.ok || !result.success) throw new Error(result.error || "Không tải được nhánh.");
  const select = $("github-branches");
  select.replaceChildren(new Option("Chọn nhánh để điền", ""));
  for (const branch of result.data) select.add(new Option(branch, branch));
  branchField.hidden = result.data.length === 0;
}

async function startGitHubConnection() {
  const response = await apiFetch("/api/v1/github/device/start", { method: "POST" });
  const result = await response.json();
  if (!response.ok || !result.success) throw new Error(result.error || "Không thể bắt đầu kết nối GitHub.");
  const device = result.data;
  githubPollInterval = device.interval;
  $("github-user-code").textContent = device.user_code;
  $("github-verify-link").href = device.verification_uri;
  $("github-device").hidden = false;
  $("github-connect-message").textContent = "Mã có hiệu lực trong " + Math.ceil(device.expires_in / 60) + " phút. Chờ bạn xác nhận trên GitHub.";
  window.clearTimeout(githubPollTimer);
  githubPollTimer = window.setTimeout(pollGitHubConnection, githubPollInterval * 1000);
}

async function pollGitHubConnection() {
  try {
    const response = await apiFetch("/api/v1/github/device/poll", { method: "POST" });
    const result = await response.json();
    if (response.status === 202 && result.data.status === "pending") {
      githubPollInterval = result.data.interval;
      githubPollTimer = window.setTimeout(pollGitHubConnection, githubPollInterval * 1000);
      return;
    }
    if (!response.ok || !result.success) throw new Error(result.error || "Kết nối GitHub thất bại.");
    window.clearTimeout(githubPollTimer);
    showGitHubConnected(result.data.username);
    await loadGitHubRepositories();
  } catch (error) {
    window.clearTimeout(githubPollTimer);
    $("github-connect-message").textContent = error.message;
  }
}

const dockerfilePresets = {
  static: "# syntax=docker/dockerfile:1\nFROM nginx:alpine\nCOPY --exclude=.git* --exclude=.github --exclude=.env* --exclude=Dockerfile . /usr/share/nginx/html/",
  vite: "FROM node:22-alpine AS build\nWORKDIR /app\nCOPY package*.json ./\nRUN npm ci\nCOPY . .\nRUN npm run build\n\nFROM nginx:alpine\nCOPY --from=build /app/dist /usr/share/nginx/html",
  node: "FROM node:22-alpine\nWORKDIR /app\nCOPY package*.json ./\nRUN npm ci --omit=dev\nCOPY . .\nENV PORT=3000\nEXPOSE 3000\nCMD [\"npm\", \"start\"]",
  python: "FROM python:3.12-slim\nWORKDIR /app\nCOPY requirements.txt ./\nRUN pip install --no-cache-dir -r requirements.txt\nCOPY . .\nENV PORT=8000\nEXPOSE 8000\nCMD [\"sh\", \"-c\", \"gunicorn --bind 0.0.0.0:$PORT app:app\"]",
  go: "FROM golang:1.25-alpine AS build\nWORKDIR /src\nCOPY go.mod ./\nRUN go mod download\nCOPY . .\nRUN CGO_ENABLED=0 go build -o /server .\n\nFROM alpine:3.22\nCOPY --from=build /server /server\nENV PORT=8080\nEXPOSE 8080\nENTRYPOINT [\"/server\"]"
};

function collectPayload() {
  const appName = $("app-name").value.trim();
  $("logs-app-name").value = appName;
  const dockerfile = $("dockerfile").value;
  return {
    manifest: {
      app_name: appName,
      source_path: appName,
      container_port: Number($("container-port").value),
      host_port: Number($("host-port").value),
      restart_policy: "unless-stopped",
      memory_limit: $("memory-limit").value.trim(),
      cpu_limit: $("cpu-limit").value.trim(),
      healthcheck_path: $("healthcheck-path").value.trim(),
      environment: parseEnvironment($("environment").value),
      ...(dockerfile.trim() ? { dockerfile } : {})
    },
    git: {
      repository: $("repository").value.trim(),
      branch: $("branch").value.trim(),
      token: $("git-token").value
    },
    network: { type: $("network-type").value, interface: $("network-interface").value.trim() }
  };
}

async function readEventStream(url, signal, onEvent) {
  const response = await apiFetch(url, { headers: apiHeaders({ Accept: "text/event-stream" }), signal });
  if (!response.ok) {
    const body = await response.text();
    throw new Error("API " + response.status + ": " + body);
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  while (true) {
    const result = await reader.read();
    if (result.done) break;
    buffer += decoder.decode(result.value, { stream: true });
    let boundary;
    while ((boundary = buffer.indexOf("\n\n")) >= 0) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const data = frame.split(/\r?\n/).filter((line) => line.startsWith("data:")).map((line) => line.slice(5).trimStart()).join("\n");
      if (data) onEvent(data);
    }
  }
}

async function submitDeploy(event) {
  event.preventDefault();
  $("form-error").hidden = true;
  try {
    const payload = collectPayload();
    deployButton.disabled = true;
    deployButton.firstElementChild.textContent = "Đang đưa vào hàng đợi…";
    jobLog.textContent = "";
    setStatus("PENDING", "Đang gửi yêu cầu triển khai");
    if (jobController) jobController.abort();
    const response = await apiFetch("/api/v1/apps/deploy", {
      method: "POST",
      headers: apiHeaders({ "Content-Type": "application/json" }),
      body: JSON.stringify(payload)
    });
    const result = await response.json();
    if (!response.ok || !result.success) throw new Error(result.error || "Không thể gửi yêu cầu triển khai.");
    activeJobId = result.data.id;
    sessionStorage.setItem("deploy-last-job", activeJobId);
    $("job-id").textContent = "JOB " + activeJobId;
    $("copy-job-id").disabled = false;
    setStatus(result.data.status, result.data.message);
    jobController = new AbortController();
    activeStream = "job";
    $("stop-stream").hidden = false;
    await readEventStream("/api/v1/jobs/" + encodeURIComponent(activeJobId) + "/events", jobController.signal, (data) => {
      try {
        const eventData = JSON.parse(data);
        if (eventData.text) appendLog(jobLog, eventData.text);
      } catch {
        appendLog(jobLog, data);
      }
    });
    const statusResponse = await apiFetch("/api/v1/jobs/" + encodeURIComponent(activeJobId));
    const finalState = await statusResponse.json();
    if (finalState.success) setStatus(finalState.data.status, finalState.data.message);
  } catch (error) {
    if (error.name !== "AbortError") {
      $("form-error").textContent = error.message;
      $("form-error").hidden = false;
      appendLog(jobLog, error.message);
    }
  } finally {
    if (activeStream === "job") activeStream = "";
    deployButton.disabled = false;
    deployButton.firstElementChild.textContent = "Bắt đầu triển khai";
    $("stop-stream").hidden = true;
  }
}

async function openContainerLogs() {
  const app = $("logs-app-name").value.trim();
  if (!/^[A-Za-z0-9]+([._-][A-Za-z0-9]+)*$/.test(app)) {
    appendLog(containerLog, "Tên ứng dụng không hợp lệ.");
    return;
  }
  if (appLogController) appLogController.abort();
  containerLog.textContent = "";
  appLogController = new AbortController();
  activeStream = "logs";
  $("stop-stream").hidden = false;
  try {
    await readEventStream("/api/v1/apps/" + encodeURIComponent(app) + "/logs", appLogController.signal, (data) => appendLog(containerLog, data));
  } catch (error) {
    if (error.name !== "AbortError") appendLog(containerLog, error.message);
  } finally {
    if (activeStream === "logs") activeStream = "";
    $("stop-stream").hidden = true;
  }
}

function restorePreviousJob() {
  const previousJob = sessionStorage.getItem("deploy-last-job");
  if (!previousJob) return;
  activeJobId = previousJob;
  $("job-id").textContent = "JOB " + previousJob;
  $("copy-job-id").disabled = false;
  apiFetch("/api/v1/jobs/" + encodeURIComponent(previousJob))
    .then((response) => response.json())
    .then((result) => { if (result.success) setStatus(result.data.status, result.data.message); })
    .catch(() => {});
}

$("network-type").addEventListener("change", (event) => {
  $("network-interface").value = event.target.value === "tailscale" ? "tailscale0" : "wg0";
});
$("project-preset").addEventListener("change", (event) => {
  $("dockerfile").value = dockerfilePresets[event.target.value] || "";
});
$("github-connect-button").addEventListener("click", async () => {
  $("github-connect-button").disabled = true;
  try {
    if (githubConnected) {
      const response = await apiFetch("/api/v1/github/disconnect", { method: "POST" });
      if (!response.ok) throw new Error("Không thể ngắt kết nối GitHub.");
      githubConnected = false;
      githubRepositories.clear();
      $("github-repository-field").hidden = true;
      $("github-branch-field").hidden = true;
      $("github-connect-title").textContent = "Chọn repository trực tiếp từ GitHub";
      $("github-connect-message").textContent = "Đã xóa token khỏi phiên agent. URL và PAT nhập tay vẫn dùng được.";
      $("github-connect-button").textContent = "Kết nối GitHub";
    } else {
      await startGitHubConnection();
    }
  } catch (error) {
    $("github-connect-message").textContent = error.message;
  } finally {
    $("github-connect-button").disabled = false;
  }
});
$("github-repository").addEventListener("change", async (event) => {
  const repository = githubRepositories.get(event.target.value);
  $("github-branch-field").hidden = true;
  if (!repository) return;
  $("repository").value = repository.clone_url;
  $("branch").value = repository.default_branch;
  try {
    await loadGitHubBranches(repository.full_name);
  } catch (error) {
    $("github-connect-message").textContent = error.message;
  }
});
$("github-branches").addEventListener("change", (event) => {
  if (event.target.value) $("branch").value = event.target.value;
});
$("copy-github-code").addEventListener("click", async () => {
  await navigator.clipboard.writeText($("github-user-code").textContent);
});
$("deploy-form").addEventListener("submit", submitDeploy);
$("open-logs").addEventListener("click", openContainerLogs);
$("login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  $("login-error").hidden = true;
  loginButton.disabled = true;
  loginButton.firstChild.textContent = "Đang đăng nhập… ";
  try {
    const response = await fetch("/api/v1/auth/login", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: $("login-username").value.trim(), password: $("login-password").value })
    });
    const result = await response.json();
    if (!response.ok || !result.success) throw new Error(result.error || "Đăng nhập không thành công.");
    $("login-password").value = "";
    showConsole(result.data.username, false);
  } catch (error) {
    $("login-error").textContent = error.message === "Failed to fetch" ? "Không kết nối được agent." : error.message;
    $("login-error").hidden = false;
  } finally {
    loginButton.disabled = false;
    loginButton.firstChild.textContent = "Vào bảng điều khiển ";
  }
});
$("logout-button").addEventListener("click", async () => {
  try {
    window.clearTimeout(githubPollTimer);
    await fetch("/api/v1/auth/logout", { method: "POST", credentials: "same-origin" });
  } finally {
    $("login-password").value = "";
    showLogin();
  }
});
$("stop-stream").addEventListener("click", () => {
  if (activeStream === "job" && jobController) jobController.abort();
  if (activeStream === "logs" && appLogController) appLogController.abort();
});
$("copy-job-id").addEventListener("click", async () => {
  if (activeJobId) await navigator.clipboard.writeText(activeJobId);
});

restoreSession();
