/**
 * background.js — Service Worker for Cloud OS GPTAdmin Computer Link
 *
 * Manages:
 *  - Native host connection lifecycle
 *  - Pairing flow (code generation, hub communication, confirmation)
 *  - Heartbeat (30s interval via chrome.alarms)
 *  - Message routing between popup, content scripts, and native host
 *  - Automatic reconnection on disconnect
 */

// ─── Constants ───────────────────────────────────────────────────────────────

const NATIVE_HOST_NAME = "com.gptadmin.cloudos.nativehost";
const HEARTBEAT_INTERVAL_MINUTES = 0.5; // 30 seconds
const HEARTBEAT_ALARM_NAME = "cloudos-heartbeat";
const RECONNECT_DELAY_MS = 10_000; // 10 seconds
const HUB_URL_STORAGE_KEY = "cloudos_hub_url";
const COMPUTER_NAME_KEY = "cloudos_computer_name";
const PAIRING_STATE_KEY = "cloudos_pairing_state";

// ─── State ───────────────────────────────────────────────────────────────────

let nativePort = null;
let computerStatus = "disconnected"; // "disconnected" | "pairing" | "connected"
let pairingCode = null;
let pairingResolve = null;
let lastHeartbeat = null;
let reconnectTimer = null;

// ─── Helpers ─────────────────────────────────────────────────────────────────

async function getHubUrl() {
  const { [HUB_URL_STORAGE_KEY]: url } = await chrome.storage.local.get(HUB_URL_STORAGE_KEY);
  return url || "https://became.bezrabotnyi.com";
}

async function getComputerName() {
  const { [COMPUTER_NAME_KEY]: name } = await chrome.storage.local.get(COMPUTER_NAME_KEY);
  return name || "My Computer";
}

async function saveComputerName(name) {
  await chrome.storage.local.set({ [COMPUTER_NAME_KEY]: name });
}

async function saveHubUrl(url) {
  await chrome.storage.local.set({ [HUB_URL_STORAGE_KEY]: url });
}

async function savePairingState(state) {
  await chrome.storage.local.set({ [PAIRING_STATE_KEY]: state });
}

async function clearPairingState() {
  await chrome.storage.local.remove(PAIRING_STATE_KEY);
}

/** Broadcasts current state to all listeners (popup, content scripts). */
function broadcastState() {
  const state = {
    type: "state-update",
    status: computerStatus,
    pairingCode,
    lastHeartbeat,
    nativeHostConnected: nativePort !== null && nativePort.connected,
  };

  // Send to popup if open
  chrome.runtime.sendMessage(state).catch(() => {
    // Popup not open — ignore
  });
}

// ─── Native Host Connection ──────────────────────────────────────────────────

/**
 * connectNativeHost — establishes (or re-establishes) the native messaging port.
 * Returns true on success, false on failure.
 */
function connectNativeHost() {
  try {
    if (nativePort?.connected) {
      return true;
    }

    nativePort = chrome.runtime.connectNative(NATIVE_HOST_NAME);

    nativePort.onDisconnect.addListener(() => {
      console.warn("[CloudOS] Native host disconnected.", nativePort?.error?.message);
      nativePort = null;
      scheduleReconnect();
    });

    nativePort.onMessage.addListener((message) => {
      handleNativeMessage(message);
    });

    console.log("[CloudOS] Native host connected.");
    return true;
  } catch (err) {
    console.error("[CloudOS] Failed to connect native host:", err.message);
    nativePort = null;
    return false;
  }
}

/** Schedule a reconnection attempt after RECONNECT_DELAY_MS. */
function scheduleReconnect() {
  if (reconnectTimer) return;

  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    if (computerStatus !== "disconnected") {
      console.log("[CloudOS] Attempting native host reconnection…");
      connectNativeHost();
      broadcastState();
    }
  }, RECONNECT_DELAY_MS);
}

function clearReconnectTimer() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
}

// ─── Hub Communication ───────────────────────────────────────────────────────

/**
 * sendToHub — POSTs a JSON payload to the Cloud OS hub.
 * Returns the parsed response or throws on error.
 */
async function sendToHub(data) {
  const hubUrl = await getHubUrl();

  // Route to the correct Cloud OS API endpoint
  let url;
  switch (data.type) {
    case "computers.list":
      url = `${hubUrl}/api/v1/cloud-os/computers`;
      return fetch(url).then(r => r.json());
    case "computer.pair":
      url = `${hubUrl}/api/v1/cloud-os/computers/pair`;
      break;
    case "computer.heartbeat":
      url = `${hubUrl}/api/v1/cloud-os/computers/${data.computerId}/heartbeat`;
      break;
    case "computer.disconnect":
      url = `${hubUrl}/api/v1/cloud-os/computers/${data.computerId}/disconnect`;
      break;
    case "computer.ticket":
      url = `${hubUrl}/api/v1/cloud-os/computers/${data.computerId}/ticket`;
      break;
    default:
      url = `${hubUrl}/api/v1/cloud-os/bridge`;
  }

  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(data),
  });

  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(`Hub responded ${res.status}: ${text}`);
  }

  return res.json();
}

// ─── Heartbeat ───────────────────────────────────────────────────────────────

/** Starts the 30-second heartbeat alarm. */
async function startHeartbeat() {
  await chrome.alarms.create(HEARTBEAT_ALARM_NAME, {
    delayInMinutes: HEARTBEAT_INTERVAL_MINUTES,
    periodInMinutes: HEARTBEAT_INTERVAL_MINUTES,
  });
  console.log("[CloudOS] Heartbeat started.");
}

async function stopHeartbeat() {
  await chrome.alarms.clear(HEARTBEAT_ALARM_NAME);
  console.log("[CloudOS] Heartbeat stopped.");
}

async function performHeartbeat() {
  if (computerStatus !== "connected" || !computerId) {
    return;
  }

  try {
    // POST /api/v1/cloud-os/computers/{id}/heartbeat
    await sendToHub({
      type: "computer.heartbeat",
      computerId,
    });

    lastHeartbeat = new Date().toISOString();
    broadcastState();
  } catch (err) {
    console.error("[CloudOS] Heartbeat failed:", err.message);
  }
}

// ─── Pairing Flow ────────────────────────────────────────────────────────────

/**
 * pairComputer — initiates the pairing flow.
 * 1. Generates a 6-character pairing code.
 * 2. Sends it to the hub for registration.
 * 3. Polls the hub for confirmation.
 * 4. On confirmed, transitions to "connected".
 */
async function pairComputer(name) {
  if (computerStatus === "pairing") {
    throw new Error("Pairing already in progress");
  }

  // Connect native host first
  const nativeOk = connectNativeHost();
  if (!nativeOk) {
    throw new Error("Native host not available. Is the host application installed?");
  }

  computerStatus = "pairing";
  await saveComputerName(name);

  // Generate pairing code
  pairingCode = generatePairingCode();
  await savePairingState({ pairingCode, name });

  broadcastState();

  try {
    // Get system info from native host
    const sysInfo = await getSystemInfo();

    // Register pairing request with hub via Cloud OS API
    // POST /api/v1/cloud-os/computers/pair
    const pairResult = await sendToHub({
      type: "computer.pair",
      name,
      os: sysInfo?.os || "unknown",
      capabilities: ["files", "processes", "network"],
      session_id: crypto.randomUUID(),
    });

    if (pairResult.status !== "paired") {
      throw new Error(pairResult.detail || "Pairing rejected");
    }

    // Store computer ID and relay URL
    computerId = pairResult.computer.id;
    relayUrl = pairResult.relay_url;
    controlTicket = pairResult.control_ticket;

    // Pass control ticket to native host so it can connect
    // to the proxyrelay as the "agent" peer
    if (controlTicket && nativePort?.connected) {
      nativePort.postMessage({
        type: "relay.connect",
        ticket: controlTicket,
        relayUrl,
        streamType: "control",
      });
    }

    computerStatus = "connected";
    pairingCode = null;
    await clearPairingState();
    await startHeartbeat();
    broadcastState();
    return { success: true, status: "connected", computerId };
  } catch (err) {
    computerStatus = "disconnected";
    pairingCode = null;
    await clearPairingState();
    broadcastState();
    throw err;
  }
}

/** Gets system info from the native host (with timeout). */
function getSystemInfo() {
  return new Promise((resolve) => {
    if (!nativePort?.connected) { resolve(null); return; }
    const timeout = setTimeout(() => resolve(null), 3000);
    const listener = (msg) => {
      if (msg.type === "system.info.response") {
        chrome.runtime.onMessage.removeListener(listener);
        clearTimeout(timeout);
        resolve(msg.data);
      }
    };
    chrome.runtime.onMessage.addListener(listener);
    nativePort.postMessage({ type: "system.info" });
  });
}

// Computer ID and relay state (set after pairing)
let computerId = null;
let relayUrl = null;
let controlTicket = null;

function generatePairingCode() {
  const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"; // No O/0/I/1 confusion
  const array = new Uint8Array(6);
  crypto.getRandomValues(array);
  return Array.from(array, (b) => chars[b % chars.length]).join("");
}

/** Polls the hub every 3s for up to 5 minutes to check pairing confirmation. */
function pollForPairingConfirmation(code) {
  return new Promise((resolve, reject) => {
    const TIMEOUT_MS = 5 * 60 * 1000; // 5 minutes
    const POLL_INTERVAL_MS = 3000;
    const start = Date.now();

    const timer = setInterval(async () => {
      if (Date.now() - start > TIMEOUT_MS) {
        clearInterval(timer);
        resolve(false);
        return;
      }

      try {
        const hubUrl = await getHubUrl();
        const res = await fetch(`${hubUrl}/api/cloudos/pair/check?code=${code}`);
        const data = await res.json();

        if (data.confirmed) {
          clearInterval(timer);
          resolve(true);
        } else if (data.rejected) {
          clearInterval(timer);
          resolve(false);
        }
      } catch {
        // Network error — keep polling
      }
    }, POLL_INTERVAL_MS);
  });
}

// ─── Disconnect ──────────────────────────────────────────────────────────────

async function disconnectComputer() {
  clearReconnectTimer();

  try {
    if (computerId) {
      // POST /api/v1/cloud-os/computers/{id}/disconnect
      await sendToHub({
        type: "computer.disconnect",
        computerId,
      });
    }
  } catch {
    // Best-effort notification to hub
  }

  if (nativePort) {
    try { nativePort.disconnect(); } catch { /* ignore */ }
    nativePort = null;
  }

  await stopHeartbeat();

  computerId = null;
  relayUrl = null;
  controlTicket = null;
  computerStatus = "disconnected";
  pairingCode = null;
  lastHeartbeat = null;
  await clearPairingState();

  broadcastState();
}

// ─── Message Handling ─────────────────────────────────────────────────────────

/** Handles incoming messages from the native host. */
function handleNativeMessage(msg) {
  console.log("[CloudOS] Native message:", msg.type, msg);

  // Store system info if received
  if (msg.type === "system.info.response") {
 chrome.storage.local.set({ cloudos_system_info: msg.data });
  }

  // Forward to popup and any open content script tabs
  chrome.runtime.sendMessage({ ...msg, source: "native" }).catch(() => {});
}

/**
 * handleMessage — central router for all incoming messages.
 * Source: popup (chrome.runtime.onMessage),
 *         content scripts (chrome.runtime.onMessage),
 *         external pages (chrome.runtime.onMessageExternal).
 */
async function handleMessage(msg, sender, sendResponse) {
  try {
    switch (msg.type) {
      // ── Status queries ──
      case "get-state": {
        const computerName = await getComputerName();
        const hubUrl = await getHubUrl();
        sendResponse({
          status: computerStatus,
          pairingCode,
          lastHeartbeat,
          computerName,
          hubUrl,
          nativeHostConnected: nativePort !== null && nativePort.connected,
        });
        return false; // synchronous response
      }

      case "get-computers": {
        const data = await sendToHub({ type: "computers.list" });
        sendResponse(data);
        return false;
      }

      // ── Pairing ──
      case "pair-computer": {
        const result = await pairComputer(msg.name);
        sendResponse(result);
        return false;
      }

      // ── Disconnect ──
      case "disconnect-computer": {
        await disconnectComputer();
        sendResponse({ success: true, status: "disconnected" });
        return false;
      }

      // ── Configuration ──
      case "set-hub-url": {
        await saveHubUrl(msg.url);
        sendResponse({ success: true, url: msg.url });
        return false;
      }

      case "set-computer-name": {
        await saveComputerName(msg.name);
        sendResponse({ success: true, name: msg.name });
        return false;
      }

      // ── Native host proxy (from content script / page) ──
      case "native-request": {
        if (!nativePort?.connected) {
          sendResponse({ error: "Native host not connected" });
          return false;
        }

        // Forward to native host, collect response via one-shot listener
        const requestId = msg.requestId ?? crypto.randomUUID();
        const payload = { ...msg.payload, requestId };

        const response = await new Promise((resolve) => {
          const listener = (responseMsg) => {
            if (responseMsg.requestId === requestId) {
              chrome.runtime.onMessage.removeListener(listener);
              resolve(responseMsg);
            }
          };
          chrome.runtime.onMessage.addListener(listener);
          nativePort.postMessage(payload);

          // Timeout after 30s
          setTimeout(() => {
            chrome.runtime.onMessage.removeListener(listener);
            resolve({ error: "Native host request timed out" });
          }, 30_000);
        });

        sendResponse(response);
        return false;
      }

      default:
        console.warn("[CloudOS] Unknown message type:", msg.type);
        sendResponse({ error: `Unknown message type: ${msg.type}` });
        return false;
    }
  } catch (err) {
    console.error("[CloudOS] Message handler error:", err);
    sendResponse({ error: err.message });
    return false;
  }
}

// ─── Event Listeners ─────────────────────────────────────────────────────────

// Messages from popup, content scripts, and external pages
chrome.runtime.onMessage.addListener(handleMessage);
chrome.runtime.onMessageExternal.addListener(handleMessage);

// Alarm-based heartbeat
chrome.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name === HEARTBEAT_ALARM_NAME) {
    await performHeartbeat();
  }
});

// Restore state on service worker startup
chrome.runtime.onInstalled.addListener(async () => {
  console.log("[CloudOS] Extension installed/updated.");
  await disconnectComputer();
});

chrome.runtime.onStartup.addListener(async () => {
  console.log("[CloudOS] Browser started — checking saved state.");
  const { [PAIRING_STATE_KEY]: savedState } = await chrome.storage.local.get(PAIRING_STATE_KEY);
  if (savedState?.pairingCode) {
    // Previous pairing was interrupted — reset
    await clearPairingState();
  }
  computerStatus = "disconnected";
  broadcastState();
});

// ─── Init ────────────────────────────────────────────────────────────────────

console.log("[CloudOS] Service worker loaded.");
