import { StrictMode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";

afterEach(() => { cleanup(); vi.restoreAllMocks(); window.history.replaceState(null, "", "#instructions"); });

function response(body: object) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

// The connect code carries the shared agent bearer, so the panel must fetch
// it only after the operator explicitly asks for it.
it("reveals the shell MCP connect code on demand from the agents screen", async () => {
  window.history.replaceState(null, "", "#agents");
  let connectInfoCalls = 0;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers: [{ server_id: "shell:fixture", status: "online", meta: {} }] });
    if (path === "/admin/api/agents/connect-info") {
      connectInfoCalls += 1;
      return response({
        hub_url: "https://hub.example",
        mcp_endpoint: "https://hub.example/mcp",
        shell_token: "agent-secret",
        transport: "polling",
        install_command: "curl -s https://install.example/deploy.sh | bash",
        env_block: "HUB_URL=https://hub.example\nSHELL_TOKEN=agent-secret\nSHELL_NAME=new-agent\nSHELLMCP_TRANSPORT=polling",
      });
    }
    throw new Error(`Unexpected request ${path}`);
  });

  render(<StrictMode><App /></StrictMode>);
  expect(await screen.findByRole("heading", { name: "Подключить новый shell MCP" })).toBeInTheDocument();
  expect(connectInfoCalls).toBe(0);
  expect(screen.queryByText(/SHELL_TOKEN=agent-secret/)).toBeNull();

  await userEvent.click(screen.getByRole("button", { name: "Показать код подключения" }));
  expect(await screen.findByText(/SHELL_TOKEN=agent-secret/)).toBeInTheDocument();
  expect(screen.getByText("curl -s https://install.example/deploy.sh | bash")).toBeInTheDocument();
  expect(connectInfoCalls).toBe(1);
  expect(screen.getByRole("button", { name: "Копировать переменные" })).toBeInTheDocument();

  await userEvent.click(screen.getByRole("button", { name: "Скрыть" }));
  expect(screen.queryByText(/SHELL_TOKEN=agent-secret/)).toBeNull();
});
