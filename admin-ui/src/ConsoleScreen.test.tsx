import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import ConsoleScreen from "./ConsoleScreen";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("keeps application console separate from audit and filters actual journal levels", async () => {
  const request = vi.spyOn(globalThis, "fetch").mockImplementation(async () => new Response(JSON.stringify({
    unit: "gptadmin-hub.service", count: 2,
    entries: [
      { time: "2026-10-31T14:00:00Z", level: "info", message: "Hub started", unit: "gptadmin-hub.service" },
      { time: "2026-10-31T14:00:01Z", level: "error", message: "connection failed", unit: "gptadmin-hub.service" },
    ],
  }), { status: 200 }));
  const user = userEvent.setup();
  render(<ConsoleScreen />);
  expect(await screen.findByText("Hub started")).toBeInTheDocument();
  expect(request.mock.calls[0][0]).toBe("/admin/api/console?limit=500");
  await user.selectOptions(screen.getByRole("combobox", { name: "Уровень логов" }), "error");
  expect(screen.getByText("connection failed")).toBeInTheDocument();
  expect(screen.queryByText("Hub started")).not.toBeInTheDocument();
  expect(screen.getByText(/технические сообщения/i)).toBeInTheDocument();
});

it("reports unavailable console instead of inventing events", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("not found", { status: 404 }));
  render(<ConsoleScreen />);
  expect(await screen.findByRole("alert")).toHaveTextContent(/не поддерживает консоль/i);
  expect(screen.queryByText("Hub started")).not.toBeInTheDocument();
});
