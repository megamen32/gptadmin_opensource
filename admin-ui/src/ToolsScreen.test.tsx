import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import ToolsScreen from "./ToolsScreen";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const success = (payload: unknown) => new Response(JSON.stringify(payload), {
  status: 200, headers: { "Content-Type": "application/json" },
});

it("uses native tools/list, tools/call and MCP Tasks without legacy calls or bearer tokens", async () => {
  const requests: { path: string; method?: string; body?: string }[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    requests.push({ path, method: init?.method, body: typeof init?.body === "string" ? init.body : undefined });
    if (path === "/admin/api/overview?limit=1") return success({ servers: [{ server_id: "shell:test", status: "online" }] });
    if (path === "/admin/api/mcp/tool") {
      const body = JSON.parse(String(init?.body)) as { method: string };
      expect(init?.credentials).toBe("same-origin");
      expect(JSON.stringify(init?.headers)).not.toContain("Authorization");
      if (body.method === "tools/list") return success({ result: { tools: [{ name: "shell_exec" }] } });
      if (body.method === "tools/call") return success({ result: { resultType: "task", taskId: "task-123", status: "working", pollIntervalMs: 1 } });
    }
    if (path === "/admin/api/tasks/task-123") return success({ resultType: "complete", taskId: "task-123", status: "completed", result: { stdout: "task-done" } });
    throw new Error("unexpected request " + path);
  });

  render(<ToolsScreen />);
  await waitFor(() => expect(screen.getByRole("button", { name: "Получить список действий" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "Получить список действий" }));
  await waitFor(() => expect(screen.getByRole("option", { name: "shell_exec" })).toBeInTheDocument());
  fireEvent.click(screen.getByRole("button", { name: "Вызвать" }));
  expect(await screen.findByText(/task-done/)).toBeInTheDocument();
  expect(requests.map((item) => item.path)).toEqual([
    "/admin/api/overview?limit=1",
    "/admin/api/mcp/tool",
    "/admin/api/mcp/tool",
    "/admin/api/tasks/task-123",
  ]);
  expect(requests[2]?.body).toContain('"method":"tools/call"');
});

it("requires a new explicit click before resubmitting after an instruction challenge", async () => {
  const calls: Record<string, unknown>[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    if (path === "/admin/api/overview?limit=1") return success({ servers: [{ server_id: "shell:test", status: "online" }] });
    if (path === "/admin/api/mcp/tool") {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      if (body.method === "tools/list") return success({ result: { tools: [{ name: "shell_exec" }] } });
      calls.push(body);
      return calls.length === 1
        ? success({ result: { resultType: "complete", isError: true, structuredContent: { executed: false, context: "approved-context", instructions: "Read these instructions", error: { code: "instructions_required" } } } })
        : success({ result: { resultType: "complete", structuredContent: { stdout: "explicit-success" } } });
    }
    throw new Error("unexpected request " + path);
  });
  render(<ToolsScreen />);
  await waitFor(() => expect(screen.getByRole("button", { name: "Получить список действий" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "Получить список действий" }));
  await waitFor(() => expect(screen.getByRole("option", { name: "shell_exec" })).toBeInTheDocument());
  fireEvent.click(screen.getByRole("button", { name: "Вызвать" }));
  const confirm = await screen.findByRole("button", { name: "Я прочитал инструкции, выполнить" });
  expect(calls).toHaveLength(1);
  fireEvent.click(confirm);
  expect(await screen.findByText(/explicit-success/)).toBeInTheDocument();
  expect(calls).toHaveLength(2);
  expect(calls[1]?.context).toBe("approved-context");
});
