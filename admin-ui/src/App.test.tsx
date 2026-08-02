import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";

const endpoint = "/admin/api/instruction-sets/default";
const initialContent = "Проверяй состояние Hub перед изменениями.";
const updatedContent = `${initialContent}\nРаботай в режиме проверки.`;
const initialFixture = {
  id: "default",
  content: initialContent,
  version: "go-100",
  updated_at: "2026-07-17T08:00:00Z",
};
const updatedFixture = {
  id: "default",
  content: updatedContent,
  version: "go-101",
  updated_at: "2026-07-17T08:05:00Z",
};

type MockOptions = {
  expectedPutContent?: string;
  initialUpdatedAt?: string | null;
  omitInitialUpdatedAt?: boolean;
  putStatus?: number;
};

function mockInstructionFetch(options: MockOptions = {}) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    expect(input).toBe(endpoint);
    expect(init?.credentials).toBe("same-origin");
    expect(new Headers(init?.headers).get("Accept")).toBe("application/json");

    if (init?.method === "PUT") {
      expect(options.expectedPutContent).toBeDefined();
      expect(new Headers(init.headers).get("Content-Type")).toBe("application/json");
      expect(new Headers(init.headers).get("If-Match")).toBe('"go-100"');
      expect(init.body).toBe(JSON.stringify({ content: options.expectedPutContent }));

      if (options.putStatus === 412) {
        return new Response(JSON.stringify({ detail: "instruction set version does not match" }), {
          status: 412,
          headers: { "Content-Type": "application/json" },
        });
      }

      return new Response(JSON.stringify({ ...updatedFixture, content: options.expectedPutContent }), {
        status: 200,
        headers: { "Content-Type": "application/json", ETag: '"go-101"' },
      });
    }

    expect(init?.method).toBeUndefined();
    const initialUpdatedAt = options.initialUpdatedAt === undefined ? initialFixture.updated_at : options.initialUpdatedAt;
    const responseFixture = options.omitInitialUpdatedAt
      ? { id: initialFixture.id, content: initialFixture.content, version: initialFixture.version }
      : { ...initialFixture, updated_at: initialUpdatedAt };
    return new Response(JSON.stringify(responseFixture), {
      status: 200,
      headers: { "Content-Type": "application/json", ETag: '"go-100"' },
    });
  });
}

afterEach(() => {
  cleanup();
  window.history.replaceState(null, "", "#instructions");
  vi.restoreAllMocks();
});

describe("Profiles / Instructions", () => {
  it("uses the exact Go contract and keeps the PUT response as the draft", async () => {
    const fetchMock = mockInstructionFetch({ expectedPutContent: updatedContent });
    render(<App />);

    const editor = await screen.findByDisplayValue(initialContent);
    fireEvent.change(editor, { target: { value: updatedContent } });
    await userEvent.click(screen.getByRole("button", { name: "Опубликовать" }));

    await waitFor(() => expect(screen.getByText("Опубликовано только что")).toBeInTheDocument());
    expect(screen.getByRole("textbox")).toHaveValue(updatedContent);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("shows a warning and blocks content above 16 KiB", async () => {
    mockInstructionFetch();
    render(<App />);

    const editor = await screen.findByRole("textbox");
    fireEvent.change(editor, { target: { value: "x".repeat(16_385) } });

    expect(screen.getByText(/Превышен лимит 16 KiB/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Опубликовать" })).toBeDisabled();
  });

  it("offers reload when the optimistic concurrency check is stale", async () => {
    const staleDraft = `${initialContent}\nИзменение.`;
    mockInstructionFetch({ expectedPutContent: staleDraft, putStatus: 412 });
    render(<App />);

    const editor = await screen.findByDisplayValue(initialContent);
    fireEvent.change(editor, { target: { value: staleDraft } });
    await userEvent.click(screen.getByRole("button", { name: "Опубликовать" }));

    expect(await screen.findByText(/Инструкции изменились на сервере/)).toBeInTheDocument();
    expect(screen.getByRole("textbox")).toHaveValue(staleDraft);
    expect(screen.getByRole("button", { name: "Загрузить актуальную версию" })).toBeInTheDocument();
  });

  it("labels built-in instructions without inventing a 1970 update time", async () => {
    mockInstructionFetch({ omitInitialUpdatedAt: true });
    render(<App />);

    expect(await screen.findByText("Встроенная версия")).toBeInTheDocument();
    expect(screen.queryByText(/1970/)).not.toBeInTheDocument();
  });

  it("keeps navigation keyboard accessible", async () => {
    mockInstructionFetch();
    render(<App />);
    await screen.findByRole("textbox");

    const current = screen.getByRole("link", { name: "Инструкции" });
    expect(current).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Профили" })).toHaveAttribute("href", "#profiles");
    expect(screen.getByRole("link", { name: "Клиенты" })).toHaveAttribute("href", "#clients");
    expect(screen.getByRole("link", { name: "Вебхуки и агенты" })).toHaveAttribute("href", "#webhooks");
    expect(screen.getByRole("link", { name: "Авторизация" })).toHaveAttribute("href", "#auth");
    expect(screen.getByRole("link", { name: "Операции и MCP" })).toHaveAttribute("href", "/admin/legacy/");
    expect(screen.getByRole("link", { name: "Выйти" })).toHaveAttribute("href", "/admin/logout");

    await userEvent.tab();
    expect(current).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Профили" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Клиенты" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Вебхуки и агенты" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Авторизация" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Операции и MCP" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Выйти" })).toHaveFocus();
  });
});

const profileFixture = {
  id: "ops",
  name: "Операционный профиль",
  access_mode: "readonly",
  allowed_targets: ["hub"],
  allowed_tools: ["discover"],
  external_workspace_refs: [{
    machine_id: "machine-a",
    workspace_path: "/srv/ops",
    startup_document: "AGENTS.md",
    shell_target: "shell:machine-a",
  }],
  version: 7,
  updated_at: "2026-07-17T08:00:00Z",
};

type ProfileFetchOptions = {
  profiles?: unknown[];
  putStatus?: number;
  failList?: boolean;
};

function mockProfileFetch(options: ProfileFetchOptions = {}) {
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    expect(init?.credentials).toBe("same-origin");
    expect(new Headers(init?.headers).get("Accept")).toBe("application/json");

    if (path === endpoint) {
      return new Response(JSON.stringify(initialFixture), {
        status: 200,
        headers: { "Content-Type": "application/json", ETag: '"go-100"' },
      });
    }
    if (path === "/admin/api/access-profiles" && !init?.method) {
      if (options.failList) return new Response(JSON.stringify({ detail: "offline" }), { status: 503 });
      return new Response(JSON.stringify({ profiles: options.profiles ?? [profileFixture] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (path === "/admin/api/access-profiles/ops" && !init?.method) {
      return new Response(JSON.stringify(profileFixture), {
        status: 200,
        headers: { "Content-Type": "application/json", ETag: '"7"' },
      });
    }
    if (path === "/admin/api/access-profiles/ops" && init?.method === "PUT") {
      expect(new Headers(init.headers).get("If-Match")).toBe('"7"');
      if (options.putStatus === 412) return new Response(JSON.stringify({ detail: "stale" }), { status: 412 });
      const body = JSON.parse(String(init.body)) as Record<string, unknown>;
      return new Response(JSON.stringify({ ...profileFixture, ...body, version: 8 }), {
        status: 200,
        headers: { "Content-Type": "application/json", ETag: '"8"' },
      });
    }
    throw new Error(`Unexpected request: ${path} ${init?.method ?? "GET"}`);
  });
  return fetchMock;
}

describe("Profiles", () => {
  it("lists, selects, and updates a profile with its ETag", async () => {
    const fetchMock = mockProfileFetch();
    render(<App />);
    await screen.findByRole("textbox", { name: "Текст инструкций" });

    await userEvent.click(screen.getByRole("link", { name: "Профили" }));
    expect(await screen.findByText("Операционный профиль")).toBeInTheDocument();
    expect(screen.getByLabelText("Режим доступа")).toHaveValue("readonly");
    expect(screen.getByLabelText("Рабочее пространство 1: путь")).toHaveValue("/srv/ops");

    const name = screen.getByLabelText("Название профиля");
    await userEvent.clear(name);
    await userEvent.type(name, "Операционный профиль v2");
    await userEvent.click(screen.getByRole("button", { name: "Сохранить профиль" }));

    expect(await screen.findByText("Профиль сохранён")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/access-profiles/ops", expect.objectContaining({ method: "PUT" }));
    expect(screen.getByLabelText("Название профиля")).toHaveValue("Операционный профиль v2");
  });

  it("creates a profile and supports workspace refs", async () => {
    mockProfileFetch({ profiles: [] });
    render(<App />);
    await screen.findByRole("textbox", { name: "Текст инструкций" });
    await userEvent.click(screen.getByRole("link", { name: "Профили" }));
    expect(await screen.findByText("Профилей пока нет")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Создать профиль" }));
    expect(screen.getByLabelText("Название профиля")).toHaveValue("");
    await userEvent.type(screen.getByLabelText("Название профиля"), "Новый профиль");
    await userEvent.click(screen.getByRole("button", { name: "Добавить рабочее пространство" }));
    expect(screen.getByLabelText("Рабочее пространство 2: путь")).toBeInTheDocument();
  });

  it("marks an optimistic-concurrency conflict as stale and offers reload", async () => {
    mockProfileFetch({ putStatus: 412 });
    render(<App />);
    await screen.findByRole("textbox", { name: "Текст инструкций" });
    await userEvent.click(screen.getByRole("link", { name: "Профили" }));
    const name = await screen.findByLabelText("Название профиля");
    await userEvent.clear(name);
    await userEvent.type(name, "Конфликтующая версия");
    await userEvent.click(screen.getByRole("button", { name: "Сохранить профиль" }));

    expect(await screen.findByText(/Профиль устарел/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Загрузить актуальную версию" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Сохранить профиль" })).toBeDisabled();
  });

  it("exposes an error state and retry without hiding navigation", async () => {
    mockProfileFetch({ failList: true });
    render(<App />);
    await screen.findByRole("textbox", { name: "Текст инструкций" });
    await userEvent.click(screen.getByRole("link", { name: "Профили" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("Не удалось загрузить профили");
    expect(screen.getByRole("button", { name: "Повторить" })).toBeEnabled();
    expect(screen.getByRole("link", { name: "Инструкции" })).toBeInTheDocument();
  });
});

describe("Clients / Auth", () => {
  it("navigates to the client inventory, issues a one-time bearer, and clears it on navigation", async () => {
    window.history.replaceState(null, "", "#clients");
    const clients = [
      { id: "jwt-1", client_id: "codex", token_kind: "managed_jwt", status: "active", access_mode: "readonly", profile_id: "ops", scope: "mcp", redirect_uris: [], issued_at: null, created_at: null, expires_at: null, revoked_at: null },
      { id: "oauth-1", client_id: "oauth-app", token_kind: "oauth", status: "active", access_mode: null, profile_id: null, scope: "mcp", redirect_uris: ["https://client.example/callback"], issued_at: null, created_at: null, expires_at: null, revoked_at: null },
      { id: "legacy-1", client_id: "legacy", token_kind: "legacy_ctl", status: "active", access_mode: null, profile_id: null, scope: null, redirect_uris: [], issued_at: null, created_at: null, expires_at: null, revoked_at: null },
    ];
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const path = String(input);
      if (path === "/admin/api/clients") return new Response(JSON.stringify({ clients }), { status: 200, headers: { "Content-Type": "application/json" } });
      if (path === "/admin/api/access-profiles") return new Response(JSON.stringify({ profiles: [profileFixture] }), { status: 200, headers: { "Content-Type": "application/json" } });
      if (path === "/admin/api/mcp/issue-token" && init?.method === "POST") return new Response(JSON.stringify({ token_id: "jwt-new", access_token: "bearer-once", token_type: "Bearer" }), { status: 200, headers: { "Content-Type": "application/json" } });
      throw new Error(`Unexpected request: ${path} ${init?.method ?? "GET"}`);
    });

    render(<App />);

    expect(await screen.findByRole("heading", { name: "Клиенты" })).toBeInTheDocument();
    expect(screen.getByText("codex")).toBeInTheDocument();
    expect(screen.getByText("oauth-app")).toBeInTheDocument();
    expect(screen.getByText("legacy")).toBeInTheDocument();
    expect(screen.queryByText("client-secret")).not.toBeInTheDocument();
    expect(screen.queryByText("bearer-once")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Выдать managed token" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("bearer-once");
    expect(screen.getByRole("button", { name: "Скопировать bearer" })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/mcp/issue-token", expect.objectContaining({ method: "POST" }));

    await userEvent.click(screen.getByRole("link", { name: "Авторизация" }));
    expect(await screen.findByRole("heading", { name: "Авторизация" })).toBeInTheDocument();
    expect(screen.queryByText("bearer-once")).not.toBeInTheDocument();
  });

  it("binds and unbinds the selected profile, rotates and revokes managed tokens, and gates unsupported actions", async () => {
    window.history.replaceState(null, "", "#clients");
    const clients = [
      { id: "jwt-1", client_id: "codex", token_kind: "managed_jwt", status: "active", access_mode: "readonly", profile_id: null, scope: "mcp", redirect_uris: [], issued_at: null, created_at: null, expires_at: null, revoked_at: null },
      { id: "oauth-1", client_id: "oauth-app", token_kind: "oauth", status: "active", access_mode: null, profile_id: null, scope: "mcp", redirect_uris: [], issued_at: null, created_at: null, expires_at: null, revoked_at: null },
    ];
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const path = String(input);
      if (path === "/admin/api/clients") return new Response(JSON.stringify({ clients }), { status: 200, headers: { "Content-Type": "application/json" } });
      if (path === "/admin/api/access-profiles") return new Response(JSON.stringify({ profiles: [profileFixture] }), { status: 200, headers: { "Content-Type": "application/json" } });
      if (path === "/admin/api/client-bindings/jwt-1" && init?.method === "PUT") return new Response(JSON.stringify({ client_id: "jwt-1", profile_id: "ops" }), { status: 200, headers: { "Content-Type": "application/json" } });
      if (path === "/admin/api/client-bindings/jwt-1" && init?.method === "DELETE") return new Response(JSON.stringify({ ok: true, unbound: true, client_id: "jwt-1" }), { status: 200, headers: { "Content-Type": "application/json" } });
      if (path === "/admin/api/mcp/tokens/jwt-1/rotate" && init?.method === "POST") return new Response(JSON.stringify({ token_id: "jwt-2", access_token: "rotated-once", token_type: "Bearer" }), { status: 200, headers: { "Content-Type": "application/json" } });
      if (path === "/admin/api/clients/jwt-1" && init?.method === "DELETE") return new Response(JSON.stringify({ ok: true, revoked: true, token_id: "jwt-1" }), { status: 200, headers: { "Content-Type": "application/json" } });
      throw new Error(`Unexpected request: ${path} ${init?.method ?? "GET"}`);
    });

    render(<App />);
    expect(await screen.findByRole("heading", { name: "Клиенты" })).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Профиль для выбранного клиента"), "ops");
    await userEvent.click(screen.getByRole("button", { name: "Привязать профиль" }));
    expect(await screen.findByText("Профиль привязан")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Снять привязку" }));
    expect(await screen.findByText("Профиль отвязан")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Ротировать токен" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("rotated-once");
    await userEvent.click(screen.getByRole("button", { name: "Отозвать токен" }));
    expect(await screen.findByText("Managed token отозван")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Выбрать oauth-app" }));
    expect(screen.getByRole("button", { name: "Ротировать токен" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Отозвать токен" })).toBeDisabled();
    expect(screen.getByText(/OAuth-клиенты управляются через Авторизация/)).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/client-bindings/jwt-1", expect.objectContaining({ method: "PUT" }));
  });
});

const webhookRouteFixture = {
  id: "repair-100",
  kind: "shell",
  target: "shell:roomhacker-server-100",
  auth_mode: "hmac",
  callback_configured: false,
};

function webhookResponse(value: unknown, status = 200): Response {
  return new Response(status === 204 ? null : JSON.stringify(value), {
    status,
    headers: status === 204 ? undefined : { "Content-Type": "application/json" },
  });
}

describe("Вебхуки и агенты", () => {
  it("lists secret-safe route summaries and retries a failed load", async () => {
    window.history.replaceState(null, "", "#webhooks");
    const fetchMock = vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(webhookResponse({ detail: "offline" }, 503))
      .mockResolvedValueOnce(webhookResponse({
        routes: [{ ...webhookRouteFixture, hmac_secret: "must-never-render", token: "must-never-render-either" }],
      }));

    render(<App />);

    expect(await screen.findByRole("alert")).toHaveTextContent("Не удалось загрузить маршруты");
    await userEvent.click(screen.getByRole("button", { name: "Повторить" }));
    expect(await screen.findByText("repair-100")).toBeInTheDocument();
    expect(screen.getByText("shell:roomhacker-server-100")).toBeInTheDocument();
    expect(screen.queryByText("must-never-render")).not.toBeInTheDocument();
    expect(screen.queryByText("must-never-render-either")).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenNthCalledWith(1, "/webhook-routes", expect.objectContaining({ credentials: "same-origin" }));
  });

  it("creates a route with a write-only secret and clears it after success", async () => {
    window.history.replaceState(null, "", "#webhooks");
    let createdBody: Record<string, unknown> | null = null;
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      if (String(input) === "/webhook-routes" && !init?.method) return webhookResponse({ routes: [] });
      if (String(input) === "/webhook-routes" && init?.method === "POST") {
        createdBody = JSON.parse(String(init.body)) as Record<string, unknown>;
        return webhookResponse(webhookRouteFixture, 201);
      }
      throw new Error(`Unexpected request: ${String(input)} ${init?.method ?? "GET"}`);
    });

    render(<App />);
    expect(await screen.findByText("Маршрутов пока нет")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Идентификатор маршрута"), "repair-100");
    await userEvent.type(screen.getByLabelText("Секрет маршрута"), "write-only-secret");
    await userEvent.selectOptions(screen.getByLabelText("Тип действия"), "shell");
    await userEvent.type(screen.getByLabelText("Цель"), "shell:roomhacker-server-100");
    await userEvent.type(screen.getByLabelText("Команда"), "fixed-helper repair_100");
    await userEvent.type(screen.getByLabelText("Рабочий каталог"), "/opt/notify");
    await userEvent.click(screen.getByRole("button", { name: "Создать маршрут" }));

    expect(await screen.findByText("Маршрут создан")).toBeInTheDocument();
    expect(screen.getByLabelText("Секрет маршрута")).toHaveValue("");
    expect(screen.queryByText("write-only-secret")).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/webhook-routes", expect.objectContaining({ method: "POST" }));
    expect(createdBody).toEqual(expect.objectContaining({
      id: "repair-100",
      hmac_secret: "write-only-secret",
      signature_version: "v2",
      action: expect.objectContaining({
        kind: "shell",
        target: "shell:roomhacker-server-100",
        command: "fixed-helper repair_100",
        cwd: "/opt/notify",
      }),
    }));
  });

  it("replaces a selected route and requires explicit confirmation before delete", async () => {
    window.history.replaceState(null, "", "#webhooks");
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const path = String(input);
      if (path === "/webhook-routes" && !init?.method) return webhookResponse({ routes: [webhookRouteFixture] });
      if (path === "/webhook-routes/repair-100" && init?.method === "PUT") return webhookResponse({ ...webhookRouteFixture, target: "shell:roomhacker-server-100-v2" });
      if (path === "/webhook-routes/repair-100" && init?.method === "DELETE") return webhookResponse(null, 204);
      throw new Error(`Unexpected request: ${path} ${init?.method ?? "GET"}`);
    });

    render(<App />);
    await userEvent.click(await screen.findByRole("button", { name: "Изменить repair-100" }));
    await userEvent.type(screen.getByLabelText("Секрет маршрута"), "replacement-secret");
    await userEvent.type(screen.getByLabelText("Команда"), "fixed-helper repair_100");
    await userEvent.clear(screen.getByLabelText("Цель"));
    await userEvent.type(screen.getByLabelText("Цель"), "shell:roomhacker-server-100-v2");
    await userEvent.click(screen.getByRole("button", { name: "Заменить маршрут" }));
    expect(await screen.findByText("Маршрут заменён")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/webhook-routes/repair-100", expect.objectContaining({ method: "PUT" }));

    await userEvent.click(screen.getByRole("button", { name: "Удалить маршрут" }));
    expect(screen.getByRole("alertdialog", { name: "Удалить маршрут repair-100?" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "Подтвердить удаление" }));
    expect(await screen.findByText("Маршрут удалён")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/webhook-routes/repair-100", expect.objectContaining({ method: "DELETE" }));
  });

  it("inspects one job by ID without rendering secret response fields", async () => {
    window.history.replaceState(null, "", "#webhooks");
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const path = String(input);
      if (path === "/webhook-routes") return webhookResponse({ routes: [] });
      if (path === "/admin/api/webhook-jobs/job-42") return webhookResponse({
        job_id: "job-42",
        route_id: "repair-100",
        status: "completed",
        created_at: "2026-08-02T06:00:00Z",
        completed_at: "2026-08-02T06:00:08Z",
        result: { session_id: "session-7", hmac_secret: "must-never-render", nested: { metadata: { access_token: "also-secret" } } },
      });
      throw new Error(`Unexpected request: ${path}`);
    });

    render(<App />);
    await screen.findByText("Маршрутов пока нет");
    await userEvent.type(screen.getByLabelText("ID задания"), "job-42");
    await userEvent.click(screen.getByRole("button", { name: "Проверить задание" }));

    expect(await screen.findByText("completed")).toBeInTheDocument();
    expect(screen.getByText("session-7")).toBeInTheDocument();
    const jobResult = screen.getByText("session-7").closest(".job-result");
    expect(jobResult).not.toHaveTextContent("must-never-render");
    expect(jobResult).not.toHaveTextContent("also-secret");
  });
});
