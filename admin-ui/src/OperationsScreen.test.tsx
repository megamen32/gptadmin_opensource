import { StrictMode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";

afterEach(() => { cleanup(); vi.restoreAllMocks(); window.history.replaceState(null, "", "#instructions"); });

function response(body: object) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

it("uses native React MCP and failover screens without the legacy runtime", async () => {
  window.history.replaceState(null, "", "#mcpmanage");
  const mcpCalls: Array<{ target: string; action: string }> = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers: [{ server_id: "shell:fixture", status: "online", meta: {} }] });
    if (path === "/admin/api/settings") return response({ settings: { stale_mcp_retention_days: 30 } });
    if (path === "/admin/api/mcp/manage" && init?.method === "POST") {
      const payload = JSON.parse(String(init.body)) as { target: string; action: string };
      mcpCalls.push(payload);
      return response({ ok: true, target: payload.target, action: payload.action, response: { complete: true, servers: [
        { server_id: "hub", name: "GPTAdmin Hub", kind: "hub", status: "online", registry_category: "hub_core", enabled: true },
        { server_id: "webhooks", name: "Webhooks", kind: "virtual_mcp", status: "disabled", registry_category: "hub_virtual_mcp", enabled: false },
        { server_id: "mcp:shell:fixture:filesystem", name: "Filesystem", kind: "child_mcp", status: "online", registry_category: "registered_runtime_child", enabled: true },
      ] } });
    }
    if (path === "/admin/api/failover") return response({ config: { enabled: false, primary_public_url: "https://saved.example", fail_count_base: 3, nodes: [] }, state: { role: "primary" } });
    throw new Error(`Unexpected request ${path}`);
  });

  render(<StrictMode><App /></StrictMode>);
  expect(await screen.findByRole("heading", { name: "Сервисы MCP" })).toBeInTheDocument();
  expect(document.querySelector("#view-mcpmanage")).toBeNull();
  expect(document.querySelector(".operations-console")).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "Обновить список" }));
  expect(await screen.findByText("GPTAdmin Hub")).toBeInTheDocument();
  expect(screen.getByText("Virtual MCP Hub")).toBeInTheDocument();
  expect(screen.getByText("Дочерний MCP из runtime-реестра")).toBeInTheDocument();
  expect(mcpCalls).toEqual([{ target: "hub", action: "list" }]);

  await userEvent.click(screen.getByRole("link", { name: "Резервирование" }));
  expect(await screen.findByRole("heading", { name: "Резервирование" })).toBeInTheDocument();
  await waitFor(() => expect(screen.getByDisplayValue("https://saved.example")).toBeInTheDocument());
  expect(screen.getByText("shell:fixture")).toBeInTheDocument();
});

it("renders advanced security as a native Settings screen", async () => {
  window.history.replaceState(null, "", "#security");
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    if (path === "/admin/api/security/preset") return response({ preset: "working_default", mfa_enrolled: false });
    if (path === "/admin/api/security/env") return response({ shellmcp_heartbeat: false });
    if (path === "/admin/api/telemetry") return response({ enabled: false, local_only: true, counters: {} });
    if (path === "/admin/api/approvals") return response({ approvals: [] });
    throw new Error(`Unexpected request ${path}`);
  });

  render(<StrictMode><App /></StrictMode>);
  expect(await screen.findByRole("heading", { name: "Безопасность" })).toBeInTheDocument();
  expect(screen.getByRole("navigation", { name: "Настройки: подразделы" })).toBeInTheDocument();
  expect(screen.getByText("Запросов нет")).toBeInTheDocument();
  expect(document.querySelector(".operations-console")).toBeNull();
});
