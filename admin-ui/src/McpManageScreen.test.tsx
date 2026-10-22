import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";

afterEach(() => { cleanup(); vi.restoreAllMocks(); window.history.replaceState(null, "", "#overview"); });

function response(body: object) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

it("shows an explicit unsupported list state for the Hub placeholder and keeps navigation usable", async () => {
  window.history.replaceState(null, "", "#mcpmanage");
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers: [{ server_id: "shell:admin-node-100", status: "online", meta: {} }] });
    if (path === "/admin/api/settings") return response({ settings: { stale_mcp_retention_days: 30 } });
    if (path === "/admin/api/mcp/manage" && init?.method === "POST") return response({
      action: "list",
      ok: true,
      target: "shell:admin-node-100",
      response: { note: "go hub MCP manage is read-only/placeholder; use shell:mcp_tools for mutation until full parity", servers: 22 },
    });
    throw new Error(`Unexpected request ${path}`);
  });

  render(<App />);
  expect(await screen.findByRole("heading", { name: "Сервисы MCP" })).toBeInTheDocument();
  await userEvent.selectOptions(screen.getAllByRole("combobox")[0], "shell:admin-node-100");
  await userEvent.click(screen.getByRole("button", { name: "Список" }));

  expect(await screen.findByText(/Hub вернул ответ без перечня сервисов/i)).toBeInTheDocument();
  expect(screen.getByText("Список недоступен")).toBeInTheDocument();
  expect(screen.queryByText("MCP не загружены")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("link", { name: "Серверы" }));
  expect(await screen.findByRole("heading", { name: "Серверы" })).toBeInTheDocument();
});

it("keeps a successful add successful when the follow-up list is unsupported", async () => {
  window.history.replaceState(null, "", "#mcpmanage");
  const actions: string[] = [];
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers: [] });
    if (path === "/admin/api/settings") return response({ settings: { stale_mcp_retention_days: 30 } });
    if (path === "/admin/api/mcp/manage") {
      const payload = JSON.parse(String(init?.body)) as { action: string };
      actions.push(payload.action);
      if (payload.action === "add") return response({ action: "add", ok: true, name: "qa23-mcp-delta" });
      return response({ action: "list", ok: true, response: { note: "placeholder", servers: 22 } });
    }
    throw new Error(`Unexpected request ${path}`);
  });

  render(<App />);
  expect(await screen.findByRole("heading", { name: "Сервисы MCP" })).toBeInTheDocument();
  await userEvent.type(screen.getByRole("textbox", { name: "Имя" }), "qa23-mcp-delta");
  await userEvent.type(screen.getByRole("textbox", { name: "Команда запуска" }), "python3");
  await userEvent.click(screen.getByRole("button", { name: "Добавить MCP" }));

  expect(await screen.findByText(/Добавление «qa23-mcp-delta» выполнено/i)).toBeInTheDocument();
  expect(screen.getByText("Список недоступен")).toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "Имя" })).toHaveValue("");
  expect(actions).toEqual(["add", "list"]);
  await userEvent.click(screen.getByRole("link", { name: "Серверы" }));
  expect(await screen.findByRole("heading", { name: "Серверы" })).toBeInTheDocument();
});

it("does not retry an add after the request outcome becomes unknown", async () => {
  window.history.replaceState(null, "", "#mcpmanage");
  let mutationCount = 0;
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    if (path.startsWith("/admin/api/overview")) return response({ servers: [] });
    if (path === "/admin/api/settings") return response({ settings: { stale_mcp_retention_days: 30 } });
    if (path === "/admin/api/mcp/manage") { mutationCount += 1; throw new TypeError("connection lost"); }
    throw new Error(`Unexpected request ${path}`);
  });

  render(<App />);
  expect(await screen.findByRole("heading", { name: "Сервисы MCP" })).toBeInTheDocument();
  await userEvent.type(screen.getByRole("textbox", { name: "Имя" }), "qa23-mcp-unknown");
  await userEvent.type(screen.getByRole("textbox", { name: "Команда запуска" }), "python3");
  await userEvent.click(screen.getByRole("button", { name: "Добавить MCP" }));

  expect(await screen.findByRole("alert")).toHaveTextContent(/Результат операции MCP неизвестен.*Запрос не повторялся/i);
  expect(screen.getByRole("button", { name: "Добавить MCP" })).toBeDisabled();
  expect(mutationCount).toBe(1);
});
