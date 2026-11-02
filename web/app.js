const $ = (id) => document.getElementById(id);
const tokenField = $("api-token");
const jobLog = $("job-log");
const containerLog = $("container-log");
const submitButton = $("deploy-button");
let jobController;
let appLogController;
let activeStream = "";
let activeJobId = "";

tokenField.value = sessionStorage.getItem("deploy-api-token") || "";
tokenField.addEventListener("input", () => sessionStorage.setItem("deploy-api-token", tokenField.value.trim()));

$("network-type").addEventListener("change", (event) => {
  $("network-interface").value = event.target.value === "tailscale" ? "tailscale0" : "wg0";
});

function apiHeaders(extra = {}) {
  const token = tokenField.value.trim();
  if (!token) throw new Error("Nhập Agent API token trước khi deploy.");
  return Object.assign({ Authorization: "Bearer " + token }, extra);
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
  badge.textContent = status;
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
    if (splitAt < 1) throw new Error("Dòng environment " + (index + 1) + " cần theo dạng KEY=VALUE.");
    const key = line.slice(0, splitAt).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) throw new Error("Environment key không hợp lệ ở dòng " + (index + 1) + ".");
    output[key] = line.slice(splitAt + 1);
  }
  return output;
}

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
      ...(dockerfile.trim() ? { dockerfile: dockerfile } : {})
    },
    git: {
      repository: $("repository").value.trim(),
      branch: $("branch").value.trim(),
      token: $("git-token").value
    },
    network: {
      type: $("network-type").value,
      interface: $("network-interface").value.trim()
    }
  };
}

async function readEventStream(url, signal, onEvent) {
    const response = await fetch(url, { headers: apiHeaders({ Accept: "text/event-stream" }), signal });
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
    apiHeaders();
    submitButton.disabled = true;
    submitButton.querySelector("span:first-child").textContent = "Queueing deployment…";
    jobLog.textContent = "";
    setStatus("PENDING", "Submitting deployment request");
    if (jobController) jobController.abort();
    const response = await fetch("/api/v1/apps/deploy", {
      method: "POST",
      headers: apiHeaders({ "Content-Type": "application/json" }),
      body: JSON.stringify(payload)
    });
    const result = await response.json();
    if (!response.ok || !result.success) throw new Error(result.error || "API " + response.status);
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
        if (eventData.status) setStatus(eventData.status, eventData.text || "");
      } catch {
        appendLog(jobLog, data);
      }
    });
    const statusResponse = await fetch("/api/v1/jobs/" + encodeURIComponent(activeJobId), { headers: apiHeaders() });
    const finalState = await statusResponse.json();
    if (finalState.success) setStatus(finalState.data.status, finalState.data.message);
  } catch (error) {
    if (error.name !== "AbortError") {
      $("form-error").textContent = error.message;
      $("form-error").hidden = false;
      setStatus("FAILED", "Request failed");
      appendLog(jobLog, error.message);
    }
  } finally {
    if (activeStream === "job") activeStream = "";
    submitButton.disabled = false;
    submitButton.querySelector("span:first-child").textContent = "Deploy application";
    $("stop-stream").hidden = true;
  }
}

async function openContainerLogs() {
  $("form-error").hidden = true;
  try {
    apiHeaders();
    const app = $("logs-app-name").value.trim();
    if (appLogController) appLogController.abort();
    containerLog.textContent = "";
    appLogController = new AbortController();
    activeStream = "logs";
    $("stop-stream").hidden = false;
    await readEventStream("/api/v1/apps/" + encodeURIComponent(app) + "/logs", appLogController.signal, (data) => appendLog(containerLog, data));
  } catch (error) {
    if (error.name !== "AbortError") appendLog(containerLog, error.message);
  } finally {
    if (activeStream === "logs") activeStream = "";
    $("stop-stream").hidden = true;
  }
}

$("deploy-form").addEventListener("submit", submitDeploy);
$("open-logs").addEventListener("click", openContainerLogs);
$("stop-stream").addEventListener("click", () => {
  if (activeStream === "job" && jobController) jobController.abort();
  if (activeStream === "logs" && appLogController) appLogController.abort();
});
$("copy-job-id").addEventListener("click", async () => {
  if (!activeJobId) return;
  await navigator.clipboard.writeText(activeJobId);
});

const previousJob = sessionStorage.getItem("deploy-last-job");
if (previousJob) {
  activeJobId = previousJob;
  $("job-id").textContent = "JOB " + previousJob;
  $("copy-job-id").disabled = false;
  if (tokenField.value) {
    fetch("/api/v1/jobs/" + encodeURIComponent(previousJob), { headers: apiHeaders() })
      .then((response) => response.json())
      .then((result) => { if (result.success) setStatus(result.data.status, result.data.message); })
      .catch(() => {});
  }
}
