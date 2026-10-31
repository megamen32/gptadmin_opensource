import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.history.replaceState(null, "", "#instructions");
});

it("keeps timeline, request logs, console, tasks and audit under one Activity navigation", async () => {
  window.history.replaceState(null, "", "#activity");
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = String(input);
    if (url.startsWith("/admin/api/audit?")) return new Response(JSON.stringify({ events: [], total: 0, clients: {} }), { status: 200 });
    if (url.startsWith("/admin/api/jobs?")) return new Response(JSON.stringify({ recent: [], count: 0 }), { status: 200 });
    if (url.startsWith("/admin/api/console?")) return new Response(JSON.stringify({ unit: "gptadmin-hub.service", entries: [], count: 0 }), { status: 200 });
    throw new Error("unexpected request: " + url);
  });
  const user = userEvent.setup();
  render(<App />);
  expect(await screen.findByText("Таймлайн операций")).toBeInTheDocument();
  for (const [name, target] of [["Запросы", "#requests"], ["Консоль", "#console"], ["Задачи", "#jobs"], ["Журнал", "#audit"]]) {
    expect(screen.getByRole("link", { name })).toHaveAttribute("href", target);
  }
  await user.click(screen.getByRole("link", { name: "Запросы" }));
  expect(await screen.findByText("Журнал запросов")).toBeInTheDocument();
  await user.click(screen.getByRole("link", { name: "Консоль" }));
  expect(await screen.findByText("Логи приложения")).toBeInTheDocument();
});
