import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import AgentsScreen from "./AgentsScreen";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

function response(body: object) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

const SERVERS = [
  { server_id: "shell:admin-server-100", name: "Shell: admin-server-100", status: "online", kind: "virtual_shell", last_seen: Date.now() / 1000, meta: {} },
  { server_id: "file:admin-server-100", name: "Files: admin-server-100", status: "online", kind: "virtual_file", last_seen: Date.now() / 1000, meta: {} },
  { server_id: "mcp:shell:admin-server-100:grepmesh", name: "grepmesh on admin-server-100", status: "failed", kind: "child_mcp", last_seen: 1000, meta: { child_ref: "grepmesh" } },
];

function stubOverview(servers: unknown[]) {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers });
    if (path === "/admin/api/agents/connect-info") return response({ detail: "not needed" });
    throw new Error(`Unexpected request ${path}`);
  });
}

it("lets the operator isolate failed nodes through the status filter", async () => {
  stubOverview(SERVERS);
  render(<AgentsScreen />);

  const filter = await screen.findByRole("combobox");
  expect(screen.getAllByText("Сервер 100")).toHaveLength(3);

  await userEvent.selectOptions(filter, "failed");

  const rows = await screen.findAllByRole("button", { name: /grepmesh/ });
  expect(rows).toHaveLength(1);
  expect(rows[0].textContent).toContain("Ошибка");
});

it("distinguishes a shell host, its file target and a child MCP node in the list row", async () => {
  stubOverview(SERVERS);
  render(<AgentsScreen />);

  const rows = await screen.findAllByRole("button", { name: /Сервер 100/ });
  const text = rows.map((row) => row.textContent ?? "");
  // Friendly name survives; the qualifier only says what kind of node this is.
  expect(text.every((value) => value.includes("Сервер 100"))).toBe(true);
  expect(text.some((value) => value.includes("Файлы"))).toBe(true);
  expect(text.some((value) => value.includes("MCP-компонент: grepmesh"))).toBe(true);
});