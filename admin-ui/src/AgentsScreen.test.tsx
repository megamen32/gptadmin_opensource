import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import AgentsScreen from "./AgentsScreen";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

function response(body: object) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

const SERVERS = [
  { server_id: "shell:admin-node-100", name: "Shell: admin-node-100", status: "online", kind: "virtual_shell", last_seen: Date.now() / 1000, meta: {} },
  { server_id: "file:admin-node-100", name: "Files: admin-node-100", status: "online", kind: "virtual_file", last_seen: Date.now() / 1000, meta: {} },
  { server_id: "mcp:shell:admin-node-100:grepmesh", name: "grepmesh on admin-node-100", status: "failed", kind: "child_mcp", last_seen: 1000, meta: { child_ref: "grepmesh" } },
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

it("matches the friendly display name and keeps the result count aligned", async () => {
  stubOverview(SERVERS);
  render(<AgentsScreen />);

  await screen.findByRole("combobox");
  await userEvent.type(screen.getByPlaceholderText("Фильтр серверов"), "Сервер 100");

  expect(await screen.findAllByRole("button", { name: /Сервер 100/ })).toHaveLength(3);
  expect(screen.getByText("3 узлов")).toBeTruthy();
});

it("includes stale nodes in unavailable, while stale remains its own filter", async () => {
  stubOverview([
    { server_id: "shell:admin-node-100", name: "Shell: admin-node-100", status: "stale", last_seen: 1789300000 },
    { server_id: "file:admin-node-100", name: "Files: admin-node-100", status: "offline", last_seen: 1789300000 },
    { server_id: "hub", name: "GPTAdmin Hub", status: "online", last_seen: 1789300000 },
  ]);
  render(<AgentsScreen />);

  const statusFilter = await screen.findByRole("combobox");
  await userEvent.selectOptions(statusFilter, "offline");

  expect(await screen.findAllByRole("button", { name: /Сервер 100/ })).toHaveLength(2);
  expect(screen.getByText("2 узлов")).toBeTruthy();

  await userEvent.selectOptions(statusFilter, "stale");
  expect(await screen.findAllByRole("button", { name: /Сервер 100/ })).toHaveLength(1);
  expect(screen.getByText("1 узлов")).toBeTruthy();
});

it("shows a missing last response as never answered instead of the Unix epoch", async () => {
  stubOverview([
    { server_id: "hub", name: "Control:Hub", status: "online", last_seen: 0 },
  ]);
  render(<AgentsScreen />);

  const row = await screen.findByRole("button", { name: /Control:Hub/ });
  expect(row.textContent).toContain("Ещё не отвечал");
  await userEvent.click(row);

  expect(screen.getAllByText("Ещё не отвечал")).toHaveLength(2);
  expect(screen.queryByText(/01\.01\.1970/)).toBeNull();
});
