const nodes = document.querySelector("#nodes");
const status = document.querySelector("#status");

function message(payload) {
  return new Promise((resolve) => chrome.runtime.sendMessage(payload, resolve));
}

function showError(error) {
  status.className = "error";
  status.textContent = error;
}

async function load() {
  const response = await message({ type: "nodes" });
  if (!response?.ok) return showError(response?.error || "Local companion is unavailable");
  const list = response.result?.nodes || [];
  nodes.replaceChildren();
  for (const node of list) {
    const option = document.createElement("option");
    option.value = node.agent_id;
    option.textContent = node.name || node.agent_id;
    nodes.append(option);
  }
  if (!list.length) showError("No authorized ShellMCP exit nodes are available");
}

document.querySelector("#connect").addEventListener("click", async () => {
  const response = await message({ type: "select", agentId: nodes.value });
  if (!response?.ok) return showError(response?.error || "Could not select exit node");
  status.className = "muted";
  status.textContent = `Chrome and other apps can use 127.0.0.1:3126 via ${nodes.selectedOptions[0]?.textContent || nodes.value}.`;
});

document.querySelector("#disable").addEventListener("click", async () => {
  const response = await message({ type: "disable" });
  if (!response?.ok) return showError(response?.error || "Could not disable Chrome proxy");
  status.className = "muted";
  status.textContent = "Chrome proxy disabled.";
});

load();
