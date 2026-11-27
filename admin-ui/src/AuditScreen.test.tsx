import { afterEach, it, expect, vi } from "vitest";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import AuditScreen from "./AuditScreen";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const events = [
  {
    time: "2026-10-23T01:03:00+03:00",
    name: "cloudos_auth_denied",
    fields: { host: "mcp.example", path: "/heartbeat", server_id: "node-a", client_id: "consumer-a", method: "POST", status: 403, detail: "first", trace_id: "trace-a" }
  },
  {
    time: "2026-10-23T01:02:00+03:00",
    name: "cloudos_auth_denied",
    fields: { host: "mcp.example", path: "/pair", server_id: "node-a", client_id: "consumer-a", method: "POST", status: 403 }
  },
  {
    time: "2026-10-23T01:01:00+03:00",
    name: "cloudos_auth_denied",
    fields: { host: "other.example", path: "/heartbeat", server_id: "node-b", client_id: "consumer-b", method: "POST", status: 403 }
  }
];

function mockAudit() {
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({ events, total: events.length }), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  }));
  return fetchMock;
}

it("shows the newest audit records first and requests the full recent window", async () => {
  const fetchMock = mockAudit();
  render(<AuditScreen />);

  const first = (await screen.findAllByRole("article"))[0];
  expect(within(first).getByText("cloudos_auth_denied")).toBeInTheDocument();
  expect(within(first).getByText(/01:03:00/)).toBeInTheDocument();
  expect(fetchMock).toHaveBeenCalledWith("/admin/api/audit?limit=500&offset=0", expect.anything());
});

it("searches the retained history through server q when the match is outside the newest page", async () => {
  const historicEvent = {
    time: "2026-10-20T10:00:00Z",
    name: "historic_match",
    fields: { path: "/retained-history-only" }
  };
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = new URL(String(input), "http://localhost");
    const historicQuery = url.searchParams.get("q") === "retained-history-only";
    const body = historicQuery ? { events: [historicEvent], total: 1 } : { events, total: 701 };
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  const user = userEvent.setup();
  render(<AuditScreen />);

  await screen.findAllByRole("article");
  await user.type(screen.getByLabelText("Поиск"), "retained-history-only");
  await user.click(screen.getByRole("button", { name: "Найти" }));

  expect(await screen.findByText("historic_match")).toBeInTheDocument();
  expect(screen.queryByText("cloudos_auth_denied")).not.toBeInTheDocument();
  expect(fetchMock).toHaveBeenLastCalledWith("/admin/api/audit?limit=500&offset=0&q=retained-history-only", expect.anything());
});

it("requests the newest window when the journal contains more than 500 records", async () => {
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = new URL(String(input), "http://localhost");
    const page = url.searchParams.get("offset") === "0"
      ? [{ ...events[2], time: "2026-10-22T23:00:00+03:00" }]
      : events;
    return new Response(JSON.stringify({ events: page, total: 701 }), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  render(<AuditScreen />);

  const rows = await screen.findAllByRole("article");
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(fetchMock).toHaveBeenNthCalledWith(2, "/admin/api/audit?limit=500&offset=201", expect.anything());
  expect(within(rows[0]).getByText(/01:03:00/)).toBeInTheDocument();
  expect(screen.getByText("3 / 3 из 701 всего")).toBeInTheDocument();

  await userEvent.click(screen.getByRole("button", { name: "Показать более ранние записи" }));
  expect(await screen.findAllByRole("article")).toHaveLength(4);
  expect(fetchMock).toHaveBeenNthCalledWith(3, "/admin/api/audit?limit=201&offset=0", expect.anything());
});

it("keeps server q on both the latest and earlier search-result pages", async () => {
  const historicEvent = { time: "2026-10-20T10:00:00Z", name: "historic_match", fields: { path: "/older-result" } };
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = new URL(String(input), "http://localhost");
    const isSearch = url.searchParams.get("q") === "multi-page-query";
    const offset = url.searchParams.get("offset");
    const limit = url.searchParams.get("limit");
    if (!isSearch) return new Response(JSON.stringify({ events, total: events.length }), { status: 200 });
    if (limit === "201" && offset === "0") return new Response(JSON.stringify({ events: [historicEvent], total: 701 }), { status: 200 });
    return new Response(JSON.stringify({ events, total: 701 }), { status: 200 });
  });
  const user = userEvent.setup();
  render(<AuditScreen />);

  await screen.findAllByRole("article");
  await user.type(screen.getByLabelText("Поиск"), "multi-page-query");
  await user.click(screen.getByRole("button", { name: "Найти" }));

  expect(await screen.findByText("3 / 3 из 701 всего")).toBeInTheDocument();
  expect(fetchMock).toHaveBeenLastCalledWith("/admin/api/audit?limit=500&offset=201&q=multi-page-query", expect.anything());
  await user.click(screen.getByRole("button", { name: "Показать более ранние записи" }));

  expect(await screen.findByText("historic_match")).toBeInTheDocument();
  expect(fetchMock).toHaveBeenLastCalledWith("/admin/api/audit?limit=201&offset=0&q=multi-page-query", expect.anything());
});

it("reset replaces an in-flight query and prevents its stale response from winning", async () => {
  const currentResult = { time: "2026-10-23T01:04:00+03:00", name: "current_query_result", fields: {} };
  const staleResult = { time: "2026-10-22T23:00:00+03:00", name: "stale_query_result", fields: {} };
  let resolveStale: ((response: Response) => void) | null = null;
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const q = new URL(String(input), "http://localhost").searchParams.get("q");
    if (q === "stale-term") return new Promise<Response>((resolve) => { resolveStale = resolve; });
    const body = q === "current-term" ? { events: [currentResult], total: 1 } : { events, total: events.length };
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }));
  });
  const user = userEvent.setup();
  render(<AuditScreen />);

  await screen.findAllByRole("article");
  const query = screen.getByLabelText("Поиск");
  await user.type(query, "stale-term");
  await user.click(screen.getByRole("button", { name: "Найти" }));
  expect(resolveStale).not.toBeNull();

  await user.clear(query);
  await user.type(query, "current-term");
  await user.click(screen.getByRole("button", { name: "Найти" }));
  expect(await screen.findByText("current_query_result")).toBeInTheDocument();
  await act(async () => {
    resolveStale?.(new Response(JSON.stringify({ events: [staleResult], total: 1 }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(screen.queryByText("stale_query_result")).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Сбросить" }));
  expect(await screen.findAllByRole("article")).toHaveLength(3);
  expect(query).toHaveValue("");
  expect(fetchMock).toHaveBeenLastCalledWith("/admin/api/audit?limit=500&offset=0", expect.anything());
});

it("combines host, endpoint, server, and consumer filters against real event fields", async () => {
  const fetchMock = mockAudit();
  const user = userEvent.setup();
  render(<AuditScreen />);

  await screen.findAllByText("/heartbeat");
  await user.type(screen.getByLabelText("Поиск"), "cloudos_auth_denied");
  await user.type(screen.getByLabelText("Хост MCP"), "mcp.example");
  await user.type(screen.getByLabelText("Endpoint"), "heartbeat");
  await user.type(screen.getByLabelText("Сервер"), "node-a");
  await user.type(screen.getByLabelText("Потребитель"), "consumer-a");
  await user.click(screen.getByRole("button", { name: "Найти" }));

  expect(screen.getAllByRole("article")).toHaveLength(1);
  expect(within(screen.getByRole("article")).getAllByText("/heartbeat")[0]).toBeInTheDocument();
  expect(screen.queryByText("/pair")).not.toBeInTheDocument();
  expect(screen.getByText("1 / 3")).toBeInTheDocument();
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(fetchMock).toHaveBeenLastCalledWith("/admin/api/audit?limit=500&offset=0&q=cloudos_auth_denied", expect.anything());
});

it("expands a record to show its remaining sanitized fields", async () => {
  mockAudit();
  const user = userEvent.setup();
  render(<AuditScreen />);

  const rows = await screen.findAllByRole("article");
  const trace = within(rows[0]).getByText("trace-a");
  expect(trace).not.toBeVisible();
  await user.click(screen.getByText("Подробности события cloudos_auth_denied"));
  expect(trace).toBeVisible();
});
