const CONTROL_ORIGIN = "http://127.0.0.1:3127";
const LOCAL_PROXY = { scheme: "http", host: "127.0.0.1", port: 3126 };

async function control(path, options = {}) {
  const response = await fetch(`${CONTROL_ORIGIN}${path}`, {
    ...options,
    headers: { "content-type": "application/json", ...(options.headers || {}) },
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(payload.detail || `Local companion returned HTTP ${response.status}`);
  return payload;
}

async function enableChromeProxy() {
  await chrome.proxy.settings.set({
    scope: "regular",
    value: {
      mode: "fixed_servers",
      rules: { singleProxy: LOCAL_PROXY, bypassList: ["127.0.0.1", "localhost"] },
    },
  });
}

async function selectNode(agentId) {
  if (typeof agentId !== "string" || agentId.trim() === "") throw new Error("An exit node is required");
  const selection = await control("/v1/selection", {
    method: "PUT",
    body: JSON.stringify({ agent_id: agentId }),
  });
  await enableChromeProxy();
  await chrome.storage.local.set({ selectedAgentId: agentId });
  return selection;
}

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  (async () => {
    switch (message?.type) {
      case "nodes":
        return control("/v1/exit-nodes");
      case "select":
        return selectNode(message.agentId);
      case "status":
        return control("/v1/status");
      case "disable":
        await chrome.proxy.settings.clear({ scope: "regular" });
        await chrome.storage.local.remove("selectedAgentId");
        return { enabled: false };
      default:
        throw new Error("Unsupported extension action");
    }
  })().then((result) => sendResponse({ ok: true, result }), (error) => sendResponse({ ok: false, error: error.message }));
  return true;
});
