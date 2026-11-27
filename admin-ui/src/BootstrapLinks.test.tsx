import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";
import BootstrapLinks from "./BootstrapLinks";
import type { BootstrapLink } from "./api";

const endpoint = "/admin/api/bootstrap-links";
const profile = { id: "sandbox", name: "Песочница", access_mode: "full", allowed_targets: ["shell:node-100"], allowed_tools: [], workspace_refs: [], version: 1 };
const link: BootstrapLink = {
  id: "grant-1", label: "Z.ai QA", profile_id: "sandbox", client_id: "bootstrap-1", token_id: "token-1",
  created_at: 1794106000, expires_at: 2100000000, revoked_at: 0, status: "active",
};
const url = `https://admin.example.test/bootstrap/${"x".repeat(43)}`;
const created = { link, url, prompt: `Прочитай инструкции по ссылке ${url} и выполни задачу.`, download_url: `${url}/client.py` };

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function mockFetch(options: { items?: BootstrapLink[]; profiles?: typeof profile[]; post?: () => Promise<Response>; deleteResponse?: Response } = {}) {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    expect(init?.credentials).toBe("same-origin");
    if (input === "/admin/api/access-profiles") return json({ profiles: options.profiles ?? [profile] });
    expect(init?.cache).toBe("no-store");
    if (input === endpoint && init?.method === "POST") return options.post ? options.post() : json(created, 201);
    if (input === endpoint && !init?.method) return json({ items: options.items ?? [] });
    if (input === `${endpoint}/grant-1` && init?.method === "DELETE") return options.deleteResponse ?? json({ ok: true });
    throw new Error(`Unexpected request: ${String(input)}`);
  });
}

async function fillForm(days = "7") {
  await screen.findByRole("option", { name: "Песочница · чтение и изменения" });
  fireEvent.change(screen.getByLabelText("Название подключения"), { target: { value: "Z.ai QA" } });
  await userEvent.selectOptions(screen.getByLabelText("Профиль доступа"), "sandbox");
  fireEvent.change(screen.getByLabelText("Срок действия, дней"), { target: { value: days } });
}

beforeEach(() => {
  window.history.replaceState(null, "", "#bootstrap");
  localStorage.clear();
  sessionStorage.clear();
});

afterEach(() => {
  cleanup();
  window.history.replaceState(null, "", "#overview");
  vi.restoreAllMocks();
});

it("opens from access navigation, creates a seven-day link and copies the ready message without storing it", async () => {
  const fetchMock = mockFetch();
  const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) };
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: clipboard });
  render(<App />);

  expect(screen.getByRole("heading", { level: 1, name: "Внешние агенты" })).toBeInTheDocument();
  expect(within(screen.getByRole("navigation", { name: "Доступ: подразделы" })).getByRole("link", { name: "Внешние агенты" })).toHaveAttribute("aria-current", "page");
  expect(screen.getByLabelText("Срок действия, дней")).toHaveValue(7);
  expect(screen.getByRole("button", { name: "Создать ссылку" })).toBeDisabled();
  await fillForm();
  await userEvent.click(screen.getByRole("button", { name: "Создать ссылку" }));

  expect(await screen.findByLabelText("Ссылка на инструкции и клиент")).toHaveValue(url);
  const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
  expect(post?.[1]?.body).toBe(JSON.stringify({ label: "Z.ai QA", profile_id: "sandbox", ttl_days: 7 }));
  expect(new Headers(post?.[1]?.headers).get("Content-Type")).toBe("application/json");
  await userEvent.click(screen.getByRole("button", { name: "Копировать сообщение" }));
  expect(clipboard.writeText).toHaveBeenCalledWith(created.prompt);
  expect(await screen.findByText("Сообщение скопировано. Вставьте его в чат с внешним агентом.")).toBeInTheDocument();
  expect(JSON.stringify(localStorage)).not.toContain(url);
  expect(JSON.stringify(sessionStorage)).not.toContain(url);
});

it("uses the configured TTL and removes the issued URL when its access is revoked", async () => {
  const fetchMock = mockFetch();
  render(<BootstrapLinks />);
  await fillForm("30");
  await userEvent.click(screen.getByRole("button", { name: "Создать ссылку" }));
  expect(await screen.findByLabelText("Ссылка на инструкции и клиент")).toHaveValue(url);
  await userEvent.click(screen.getByRole("button", { name: "Отозвать доступ «Z.ai QA»" }));

  expect(await screen.findByText("Отозвана")).toBeInTheDocument();
  expect(screen.queryByLabelText("Ссылка на инструкции и клиент")).not.toBeInTheDocument();
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "DELETE")).toHaveLength(1);
  expect(fetchMock.mock.calls.find(([, init]) => init?.method === "POST")?.[1]?.body).toBe(JSON.stringify({ label: "Z.ai QA", profile_id: "sandbox", ttl_days: 30 }));
  expect(screen.queryByRole("button", { name: "Отозвать доступ «Z.ai QA»" })).not.toBeInTheDocument();
});

it("does not invent URLs for existing links and shows expiry and revocation separately", async () => {
  mockFetch({ items: [
    link,
    { ...link, id: "expired", label: "Старая песочница", expires_at: 1 },
    { ...link, id: "revoked", label: "Закрытая песочница", revoked_at: 1794106100, status: "revoked" },
  ] });
  render(<BootstrapLinks />);

  expect(await screen.findByText("Z.ai QA")).toBeInTheDocument();
  expect(screen.getByText("Срок истёк")).toBeInTheDocument();
  expect(screen.getByText("Отозвана")).toBeInTheDocument();
  expect(screen.getAllByRole("button", { name: /Отозвать доступ/ })).toHaveLength(1);
  expect(screen.queryByLabelText("Ссылка на инструкции и клиент")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Копировать сообщение" })).not.toBeInTheDocument();
});

it("keeps an active row when revocation is denied", async () => {
  mockFetch({ items: [link], deleteResponse: json({ detail: "denied" }, 403) });
  render(<BootstrapLinks />);
  await userEvent.click(await screen.findByRole("button", { name: "Отозвать доступ «Z.ai QA»" }));

  expect(await screen.findByRole("alert")).toHaveTextContent("Для управления ссылками нужен доступ администратора.");
  expect(screen.getByText("Действует")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Отозвать доступ «Z.ai QA»" })).toBeEnabled();
});

it("requires a refresh after an uncertain creation and never retries the POST automatically", async () => {
  const fetchMock = mockFetch({ post: async () => { throw new TypeError("network error"); } });
  render(<BootstrapLinks />);
  await fillForm();
  await userEvent.click(screen.getByRole("button", { name: "Создать ссылку" }));

  expect(await screen.findByRole("alert")).toHaveTextContent("Ответ о создании не получен. Обновите список");
  expect(screen.getByRole("button", { name: "Создать ссылку" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Создать ссылку" }));
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
  await userEvent.click(screen.getByRole("button", { name: "Обновить" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Создать ссылку" })).toBeEnabled());
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
});

it.each([500, 503])("shows the server reason in a compact alert without replaying an uncertain HTTP %s creation", async (status) => {
  const reason = "configured LHC instructions must be a regular file";
  const fetchMock = mockFetch({ post: async () => json({ detail: reason }, status) });
  render(<BootstrapLinks />);
  await fillForm();
  await userEvent.click(screen.getByRole("button", { name: "Создать ссылку" }));

  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent(reason);
  expect(alert).toHaveTextContent("Ответ о создании не получен. Обновите список");
  expect(alert).toHaveClass("bootstrap-error");
  expect(alert).not.toHaveClass("state-panel");
  expect(screen.getByRole("button", { name: "Создать ссылку" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Создать ссылку" }));
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
});

it("requires an existing profile and rejects an invalid duration before sending", async () => {
  const fetchMock = mockFetch();
  render(<BootstrapLinks />);
  await fillForm("0");
  expect(screen.getByRole("button", { name: "Создать ссылку" })).toBeDisabled();
  fireEvent.change(screen.getByLabelText("Срок действия, дней"), { target: { value: "7.5" } });
  expect(screen.getByRole("button", { name: "Создать ссылку" })).toBeDisabled();
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);
});

it("offers profile setup when there are no access profiles", async () => {
  mockFetch({ profiles: [] });
  render(<BootstrapLinks />);
  expect(await screen.findByRole("link", { name: "создайте профиль доступа" })).toHaveAttribute("href", "#profiles");
  expect(screen.getByRole("button", { name: "Создать ссылку" })).toBeDisabled();
});
