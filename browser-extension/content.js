/**
 * content.js — Content script injected into Cloud OS pages.
 *
 * Bridges the web page ↔ extension background service worker.
 * Exposes `window.cloudOS` API for the Browser OS UI to use.
 */

// ── Page ↔ Content Script bridge ────────────────────────────────────────────

/**
 * Send a message to the extension background and return the response.
 * Rejects if the background returns an error or times out.
 */
function sendToBackground(message, timeoutMs = 30_000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      reject(new Error("Request to extension timed out"));
    }, timeoutMs);

    chrome.runtime.sendMessage(message, (response) => {
      clearTimeout(timer);
      if (chrome.runtime.lastError) {
        reject(new Error(chrome.runtime.lastError.message));
        return;
      }
      if (response?.error) {
        reject(new Error(response.error));
        return;
      }
      resolve(response);
    });
  });
}

// ── Listen for postMessage from the page ────────────────────────────────────

window.addEventListener("message", (event) => {
  // Only accept messages from our own pages
  if (event.source !== window) return;
  if (!event.data || typeof event.data !== "object") return;
  if (!event.data.__cloudOS) return;

  const { action, id, payload } = event.data;

  // Forward to background
  sendToBackground({ type: "native-request", action, requestId: id, payload })
    .then((result) => {
      window.postMessage({ __cloudOS: true, id, result }, "*");
    })
    .catch((err) => {
      window.postMessage({ __cloudOS: true, id, error: err.message }, "*");
    });
});

// ── Expose window.cloudOS API ───────────────────────────────────────────────

window.cloudOS = {
  /**
   * getComputers — list all paired computers from the hub.
   * @returns {Promise<Array<{id: string, name: string, status: string}>>}
   */
  async getComputers() {
    const data = await sendToBackground({ type: "get-computers" });
    return data?.computers ?? [];
  },

  /**
   * readFile — read a file from a remote computer.
   * @param {string} computerId
   * @param {string} path
   * @returns {Promise<string>} file contents
   */
  async readFile(computerId, path) {
    const resp = await sendToBackground({
      type: "native-request",
      action: "fs.read",
      payload: { computerId, path },
    });
    return resp?.content;
  },

  /**
   * writeFile — write content to a file on a remote computer.
   * @param {string} computerId
   * @param {string} path
   * @param {string} content
   */
  async writeFile(computerId, path, content) {
    await sendToBackground({
      type: "native-request",
      action: "fs.write",
      payload: { computerId, path, content },
    });
  },

  /**
   * execCommand — execute a shell command on a remote computer.
   * @param {string} computerId
   * @param {string} command
   * @returns {Promise<{exitCode: number, stdout: string, stderr: string}>}
   */
  async execCommand(computerId, command) {
    return sendToBackground({
      type: "native-request",
      action: "process.exec",
      payload: { computerId, command },
    });
  },

  /**
   * listFiles — list directory contents on a remote computer.
   * @param {string} computerId
   * @param {string} path
   * @returns {Promise<Array<{name: string, type: 'file'|'directory', size?: number}>>}
   */
  async listFiles(computerId, path) {
    const resp = await sendToBackground({
      type: "native-request",
      action: "fs.list",
      payload: { computerId, path },
    });
    return resp?.entries ?? [];
  },
};

console.log("[CloudOS] Content script loaded — window.cloudOS API available.");
