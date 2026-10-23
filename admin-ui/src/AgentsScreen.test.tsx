import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
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

it("shows stale-node resources as unavailable and links to that exact node's saved latest task", async () => {
  const serverId = "shell:admin-node-100";
  const task = { job_id: "historical-task-7", server_id: serverId, status: "failed", tool_name: "system_inspect", created_at: 1780000000 };
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers: [{ server_id: serverId, name: "Shell: admin-node-100", status: "stale", last_seen: 1780000000 }] });
    if (path === "/admin/api/servers/resources") return response({
      server_id: serverId,
      status: "unavailable",
      sampled_at: null,
      node_status: "stale",
      last_seen: 1780000000,
      error: "node_not_online",
      cpu: { status: "unavailable", error: "node_not_online" },
      memory: { status: "unavailable", error: "node_not_online" },
      root_filesystem: { status: "unavailable", error: "node_not_online" },
    });
    if (path === `/admin/api/jobs?server_id=shell%3Aadmin-node-100&limit=1`) return response({ count: 1, recent: [task] });
    if (path === "/admin/api/agents/connect-info") return response({ detail: "not needed" });
    throw new Error(`Unexpected request ${path} ${init?.method ?? "GET"}`);
  });
  render(<AgentsScreen />);

  await userEvent.click(await screen.findByRole("button", { name: /Сервер 100/ }));
  const taskLink = await screen.findByRole("link", { name: /Открыть последнюю задачу/ });

  expect(screen.getByText(/Метрики недоступны/)).toBeInTheDocument();
  expect(screen.getByText(/снимок не выполнялся/)).toBeInTheDocument();
  expect(screen.getAllByText("Недоступно: node_not_online")).toHaveLength(3);
  expect(screen.queryByText(/ГиБ|%/)).toBeNull();
  expect(taskLink).toHaveAttribute("href", "#jobs?server_id=shell%3Aadmin-node-100&job_id=historical-task-7");
  expect(fetchMock).toHaveBeenCalledWith("/admin/api/servers/resources", expect.objectContaining({
    method: "POST",
    body: JSON.stringify({ server_id: serverId }),
  }));
  expect(fetchMock).toHaveBeenCalledWith("/admin/api/jobs?server_id=shell%3Aadmin-node-100&limit=1", expect.anything());
});

it("polls an admitted resource job by its existing id and renders measured values", async () => {
  const serverId = "shell:host-a";
  const summary = {
    status: "available",
    sampled_at: "2026-10-23T00:00:01Z",
    cpu: { status: "available", cores: 8, utilization_percent: 12.5, sample_interval_ms: 250 },
    memory: { status: "available", total_bytes: 16 * 1024 ** 3, available_bytes: 6 * 1024 ** 3, used_bytes: 10 * 1024 ** 3 },
    root_filesystem: { status: "available", mount: "/", total_bytes: 100 * 1024 ** 3, used_bytes: 40 * 1024 ** 3, free_bytes: 60 * 1024 ** 3 },
  };
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers: [{ server_id: serverId, name: "Shell: host-a", status: "online" }] });
    if (path === "/admin/api/servers/resources") return response({ server_id: serverId, status: "pending", job_id: "resource-job-1" });
    if (path === "/mcp-relay/job/resource-job-1?detail=full") return response({ server_id: serverId, status: "completed", response: { structuredContent: { result: summary } } });
    if (path === "/admin/api/agents/connect-info") return response({ detail: "not needed" });
    throw new Error(`Unexpected request ${path}`);
  });
  render(<AgentsScreen />);

  await userEvent.click(await screen.findByRole("button", { name: /Shell: host-a/ }));

  expect(await screen.findByText(/12,5 % · 8 ядер/)).toBeInTheDocument();
  expect(screen.getByText(/10 ГиБ из 16 ГиБ/)).toBeInTheDocument();
  expect(screen.getByText(/40 ГиБ из 100 ГиБ/)).toBeInTheDocument();
  await waitFor(() => expect(fetchMock.mock.calls.filter(([input]) => input === "/admin/api/servers/resources")).toHaveLength(1));
  expect(fetchMock).toHaveBeenCalledWith("/mcp-relay/job/resource-job-1?detail=full", expect.anything());
});
