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
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers: [{ server_id: "shell:fixture", status: "online", meta: {} }] });
    if (path === "/admin/api/settings") return response({ settings: { stale_mcp_retention_days: 30 } });
    if (path === "/admin/api/mcp/manage") return response({ servers: [{ name: "fixture-mcp", command: "python3", args: ["example.py"], env: { MODE: "test" }, enabled: true }] });
    if (path === "/admin/api/failover") return response({ config: { enabled: false, primary_public_url: "https://saved.example", fail_count_base: 3, nodes: [] }, state: { role: "primary" } });
    throw new Error(`Unexpected request ${path}`);
  });

  render(<StrictMode><App /></StrictMode>);
  expect(await screen.findByRole("heading", { name: "Сервисы MCP" })).toBeInTheDocument();
  expect(document.querySelector("#view-mcpmanage")).toBeNull();
  expect(document.querySelector(".operations-console")).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "Список" }));
  expect(await screen.findByText("fixture-mcp")).toBeInTheDocument();
  expect(screen.getAllByText(/python3/).length).toBeGreaterThan(0);

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
