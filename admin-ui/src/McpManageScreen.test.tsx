import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";

afterEach(() => { cleanup(); vi.restoreAllMocks(); window.history.replaceState(null, "", "#overview"); });

function response(body: object, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

it("lists the real Hub registry categories and refreshes a selected node's supervisor status", async () => {
  window.history.replaceState(null, "", "#mcpmanage");
  const calls: Array<{ target: string; action: string; ref?: string }> = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers: [{ server_id: "shell:node-a", status: "online", meta: {} }] });
    if (path === "/admin/api/settings") return response({ settings: { stale_mcp_retention_days: 30 } });
    if (path === "/admin/api/mcp/manage" && init?.method === "POST") {
      const payload = JSON.parse(String(init.body)) as { target: string; action: string; ref?: string };
      calls.push(payload);
      if (payload.target === "hub") return response({ ok: true, target: "hub", action: "list", response: { complete: true, servers: [
        { server_id: "hub", name: "GPTAdmin Hub", kind: "hub", status: "online", registry_category: "hub_core", enabled: true },
        { server_id: "webhooks", name: "Webhooks", kind: "virtual_mcp", status: "disabled", registry_category: "hub_virtual_mcp", enabled: false },
        { server_id: "mcp:shell:node-a:filesystem", name: "Filesystem", kind: "child_mcp", status: "online", registry_category: "registered_runtime_child", enabled: true },
      ] } });
      if (payload.action === "list") return response({ ok: true, target: payload.target, action: "list", response: { complete: true, source: "shellmcp_supervisor", servers: [
        { server_id: "filesystem", ref: "filesystem", name: "Filesystem", kind: "child_mcp_config", registry_category: "node_child_config", enabled: true, status: "configured", transport: "stdio" },
      ] } });
      return response({ ok: true, target: payload.target, action: "status", response: { agent: { ref: payload.ref }, status: { process: { state: "running" }, protocol: { state: "ready" } } } });
    }
    throw new Error(`Unexpected request ${path}`);
  });

  render(<App />);
  expect(await screen.findByRole("heading", { name: "Сервисы MCP" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Обновить список" }));
  expect(await screen.findByText("GPTAdmin Hub")).toBeInTheDocument();
  expect(screen.getByText("Virtual MCP Hub")).toBeInTheDocument();
  expect(screen.getByText("Дочерний MCP из runtime-реестра")).toBeInTheDocument();
  expect(screen.getByText("Выключен")).toBeInTheDocument();

  await userEvent.selectOptions(screen.getAllByRole("combobox")[0], "shell:node-a");
  await userEvent.click(screen.getByRole("button", { name: "Обновить список" }));
  expect(await screen.findByText("Конфигурация дочернего MCP узла")).toBeInTheDocument();
  expect(screen.getByText("Настроен")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Обновить состояние" }));
  expect(await screen.findByText("В сети")).toBeInTheDocument();
  expect(calls.map(({ target, action, ref }) => ({ target, action, ref }))).toEqual([
    { target: "hub", action: "list", ref: undefined },
    { target: "shell:node-a", action: "list", ref: undefined },
    { target: "shell:node-a", action: "status", ref: "filesystem" },
  ]);
});

it("keeps a malformed Hub response unavailable rather than treating a count as an empty list", async () => {
  window.history.replaceState(null, "", "#mcpmanage");
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers: [] });
    if (path === "/admin/api/settings") return response({ settings: { stale_mcp_retention_days: 30 } });
    if (path === "/admin/api/mcp/manage" && init?.method === "POST") return response({ ok: true, target: "hub", action: "list", response: { servers: 22 } });
    throw new Error(`Unexpected request ${path}`);
  });

  render(<App />);
  await userEvent.click(await screen.findByRole("button", { name: "Обновить список" }));
  expect(await screen.findByText(/Hub вернул ответ без перечня сервисов/i)).toBeInTheDocument();
  expect(screen.getByText("Список недоступен")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("link", { name: "Серверы" }));
  expect(await screen.findByRole("heading", { name: "Серверы" })).toBeInTheDocument();
});
