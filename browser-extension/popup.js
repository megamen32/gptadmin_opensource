/**
 * popup.js — Extension popup controller
 */

// ── DOM References ──────────────────────────────────────────────────────────

const $statusDot    = document.getElementById("statusDot");
const $statusText   = document.getElementById("statusText");
const $pairingSection = document.getElementById("pairingSection");
const $pairingCode  = document.getElementById("pairingCode");
const $computerName = document.getElementById("computerName");
const $btnConnect   = document.getElementById("btnConnect");
const $btnDisconnect = document.getElementById("btnDisconnect");
const $btnSettings  = document.getElementById("btnSettings");
const $settingsPanel = document.getElementById("settingsPanel");
const $hubUrl       = document.getElementById("hubUrl");
const $lastHeartbeat = document.getElementById("lastHeartbeat");

// ── State ───────────────────────────────────────────────────────────────────

let currentState = {
  status: "disconnected",
  pairingCode: null,
  lastHeartbeat: null,
  computerName: "",
  hubUrl: "",
  nativeHostConnected: false,
};

// ── UI Updates ──────────────────────────────────────────────────────────────

function updateUI(state) {
  currentState = { ...currentState, ...state };

  // Status dot + text
  $statusDot.className = "status-dot " + currentState.status;

  const labels = {
    disconnected: "Disconnected",
    pairing: "Pairing…",
    connected: "Connected",
  };
  $statusText.textContent = labels[currentState.status] ?? currentState.status;

  // Pairing code
  if (currentState.status === "pairing" && currentState.pairingCode) {
    $pairingSection.classList.add("visible");
    $pairingCode.textContent = currentState.pairingCode;
  } else {
    $pairingSection.classList.remove("visible");
  }

  // Buttons
  const isIdle = currentState.status === "disconnected";
  const isActive = currentState.status === "connected";
  const isPairing = currentState.status === "pairing";

  $btnConnect.disabled = !isIdle;
  $btnDisconnect.disabled = !isActive && !isPairing;
  $btnConnect.textContent = isPairing ? "Pairing…" : "Enable this Computer";

  // Computer name
  if (currentState.computerName && !$computerName.matches(":focus")) {
    $computerName.value = currentState.computerName;
  }

  // Hub URL
  if (currentState.hubUrl && !$hubUrl.matches(":focus")) {
    $hubUrl.value = currentState.hubUrl;
  }

  // Last heartbeat
  if (currentState.lastHeartbeat) {
    const ago = formatTimeSince(new Date(currentState.lastHeartbeat));
    $lastHeartbeat.textContent = `Heartbeat: ${ago}`;
  } else {
    $lastHeartbeat.textContent = "No heartbeat yet";
  }
}

function formatTimeSince(date) {
  const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  return `${Math.floor(minutes / 60)}h ago`;
}

// ── Actions ─────────────────────────────────────────────────────────────────

async function requestState() {
  try {
    const state = await chrome.runtime.sendMessage({ type: "get-state" });
    if (state) updateUI(state);
  } catch (err) {
    console.error("Failed to get state:", err);
  }
}

async function doConnect() {
  const name = $computerName.value.trim() || "My Computer";
  $btnConnect.disabled = true;
  $btnConnect.textContent = "Connecting…";

  try {
    const result = await chrome.runtime.sendMessage({ type: "pair-computer", name });
    if (result?.success) {
      updateUI({ status: "connected" });
    }
  } catch (err) {
    alert(`Connection failed: ${err.message || err}`);
    updateUI({ status: "disconnected" });
  }
}

async function doDisconnect() {
  $btnDisconnect.disabled = true;

  try {
    await chrome.runtime.sendMessage({ type: "disconnect-computer" });
    updateUI({ status: "disconnected", pairingCode: null, lastHeartbeat: null });
  } catch (err) {
    console.error("Disconnect failed:", err);
  }
}

async function saveComputerName() {
  const name = $computerName.value.trim();
  if (name) {
    await chrome.runtime.sendMessage({ type: "set-computer-name", name });
  }
}

async function saveHubUrl() {
  const url = $hubUrl.value.trim();
  if (url) {
    await chrome.runtime.sendMessage({ type: "set-hub-url", url });
  }
}

// ── Event Listeners ─────────────────────────────────────────────────────────

$btnConnect.addEventListener("click", doConnect);
$btnDisconnect.addEventListener("click", doDisconnect);

$btnSettings.addEventListener("click", () => {
  $settingsPanel.classList.toggle("visible");
});

$computerName.addEventListener("change", saveComputerName);
$hubUrl.addEventListener("change", saveHubUrl);

// Listen for state pushes from background
chrome.runtime.onMessage.addListener((msg) => {
  if (msg.type === "state-update") {
    updateUI({
      status: msg.status,
      pairingCode: msg.pairingCode,
      lastHeartbeat: msg.lastHeartbeat,
      nativeHostConnected: msg.nativeHostConnected,
    });
  }
});

// ── Init ────────────────────────────────────────────────────────────────────

requestState();
