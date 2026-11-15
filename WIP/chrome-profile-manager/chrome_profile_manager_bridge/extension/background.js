"use strict";
importScripts("config.js");
let port;
let connectionError = null;
function connect(secret, retry = true) {
  if (port) port.disconnect();
  const current = chrome.runtime.connectNative("com.gptadmin.extension_bridge");
  port = current;
  connectionError = null;
  current.onDisconnect.addListener(() => {
    connectionError = chrome.runtime.lastError?.message || "Native host disconnected";
    if (port !== current) return;
    port = null;
    // One event-triggered recovery attempt; no recurring polling on failure.
    if (retry) setTimeout(() => { if (!port) connect(secret, false); }, 1000);
  });
  current.onMessage.addListener(async request => {
    // A real request proves this connection worked and rearms recovery.
    retry = true;
    try {
      if (request.extension_id === chrome.runtime.id && ["disable", "uninstall"].includes(request.method)) {
        throw new Error("Cannot disable or uninstall the management bridge");
      }
      let result;
      switch (request.method) {
        case "list": result = await chrome.management.getAll(); break;
        case "get": result = await chrome.management.get(request.extension_id); break;
        case "enable": case "disable":
          await chrome.management.setEnabled(request.extension_id, request.method === "enable");
          result = await chrome.management.get(request.extension_id); break;
        case "uninstall":
          await chrome.management.uninstall(request.extension_id, {showConfirmDialog: true});
          result = {uninstalled: request.extension_id}; break;
        case "launch_app": await chrome.management.launchApp(request.extension_id); result = {launched: request.extension_id}; break;
        default: throw new Error("Unsupported method");
      }
      current.postMessage({id: request.id, result});
    } catch (e) { current.postMessage({id: request.id, error: String(e.message || e)}); }
  });
  current.postMessage(secret ? {type:"hello", secret} : {type:"auto"});
}
chrome.runtime.onStartup.addListener(() => { if (AUTO_CONNECT) connect(); });
chrome.runtime.onInstalled.addListener(() => { if (AUTO_CONNECT) connect(); });
chrome.runtime.onMessage.addListener((message, sender, reply) => {
  if (sender.id === chrome.runtime.id && message?.type === "status") { reply({connected: !!port, error: connectionError}); return; }
  if (sender.id !== chrome.runtime.id || message?.type !== "connect" || !/^[a-f0-9]{64}$/.test(message.secret)) return;
  connect(message.secret);
  reply({started:true});
});
