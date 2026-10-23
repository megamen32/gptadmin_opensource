import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import ProxyRouteScreen from "./ProxyRouteScreen";
import type { ProxyRouteProfile, ProxyRouteState } from "./api";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const profile: ProxyRouteProfile = {
  schema: "gptadmin.proxy-route/v1",
  id: "relay02-server44-de-http",
  consumer: "relay02",
  exit: "node-44-de-http",
  listener: { bind_address: "127.0.0.1", port: 24836 },
  ssh_identity_ref: "ssh-config:relay02",
  desired_state: "enabled",
};

const snapshot = (overrides: Partial<ProxyRouteState["runtime"]["last_probe"]> = {}): ProxyRouteState => ({
  profile: { ...profile },
  runtime: {
    profile_saved: true,
    service_state: "active",
    listener_state: "listening",
    last_probe: {
      state: "pass",
      checked_at: "2026-10-23T12:00:00Z",
      direct_exit_ip: "203.0.113.10",
      proxy_exit_ip: "203.0.113.20",
      server44_path_verified: true,
      server44_path_reason: "Established node-44 connection observed.",
      ...overrides,
    },
  },
});

it("shows live probe addresses, preserves unknown path verification, and exposes its reason", async () => {
  const load = vi.fn().mockResolvedValue(snapshot({ server44_path_verified: null, server44_path_reason: "Readback timed out." }));
  render(<ProxyRouteScreen load={load} save={vi.fn()} action={vi.fn()} />);

  expect(await screen.findByText("Путь через сервер 44 неизвестен")).toBeInTheDocument();
  expect(screen.getByText("203.0.113.10")).toBeInTheDocument();
  expect(screen.getByText("203.0.113.20")).toBeInTheDocument();
  expect(screen.getByText("Readback timed out.")).toBeInTheDocument();
  expect(load).toHaveBeenCalledTimes(1);
});

it("renders long proxy diagnostics in the dedicated readable row", async () => {
  const diagnostic = "Could not verify the established node-44 socket: timed out waiting for node-44 socket readback Direct VUSA request failed: VUSA readback failed: 185.240.120.152 Proxy request failed: VUSA readback failed: 212.192.31.128";
  render(<ProxyRouteScreen load={vi.fn().mockResolvedValue(snapshot({ server44_path_verified: null, server44_path_reason: diagnostic }))} save={vi.fn()} action={vi.fn()} />);

  expect(await screen.findByText(diagnostic)).toBeInTheDocument();
  expect(screen.getByText(diagnostic).closest(".proxy-route-diagnostic")).toBeInTheDocument();
});

it("saves the selected desired state separately from lifecycle actions", async () => {
  const save = vi.fn().mockResolvedValue(snapshot({ state: "not_run" }));
  const action = vi.fn();
  render(<ProxyRouteScreen load={vi.fn().mockResolvedValue(snapshot())} save={save} action={action} />);

  await userEvent.selectOptions(await screen.findByRole("combobox"), "disabled");
  await userEvent.click(screen.getByRole("button", { name: "Сохранить состояние" }));
  expect(save).toHaveBeenCalledWith({ ...profile, desired_state: "disabled" });
  expect(action).not.toHaveBeenCalled();
});

it("runs probe only on explicit request", async () => {
  const action = vi.fn().mockResolvedValue(snapshot());
  const initial = snapshot();
  initial.runtime.last_probe = null;
  render(<ProxyRouteScreen load={vi.fn().mockResolvedValue(initial)} save={vi.fn()} action={action} />);

  expect(await screen.findByText("Не проверено")).toBeInTheDocument();
  expect(action).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Проверить маршрут" }));
  expect(action).toHaveBeenCalledWith("probe");
});

it("refreshes the listener after a probe and keeps it unknown until the readback completes", async () => {
  let finishReadback!: (value: ProxyRouteState) => void;
  const readback = new Promise<ProxyRouteState>((resolve) => { finishReadback = resolve; });
  const actionResult = snapshot({ server44_path_verified: true });
  actionResult.runtime.listener_state = "unknown";
  const freshState = snapshot({ server44_path_verified: true });
  const load = vi.fn()
    .mockResolvedValueOnce(snapshot())
    .mockReturnValueOnce(readback);
  const action = vi.fn().mockResolvedValue(actionResult);
  render(<ProxyRouteScreen load={load} save={vi.fn()} action={action} />);

  await screen.findByText("Слушает");
  await userEvent.click(screen.getByRole("button", { name: "Проверить маршрут" }));

  expect(await screen.findByText("Проверка выполнена. Уточняем состояние маршрута…")).toBeInTheDocument();
  expect(load).toHaveBeenCalledTimes(2);
  expect(screen.getByText("Неизвестно")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Проверить маршрут" })).toBeDisabled();

  finishReadback(freshState);
  expect(await screen.findByText("Слушает")).toBeInTheDocument();
  expect(screen.getByText("Проверка выполнена. Показаны её результаты и актуальное состояние маршрута.")).toBeInTheDocument();
  expect(screen.getByText("Путь через сервер 44 подтверждён")).toBeInTheDocument();
  expect(action).toHaveBeenCalledTimes(1);
});

it("preserves probe results and unknown listener state when the post-probe readback fails", async () => {
  const actionResult = snapshot({ server44_path_verified: true });
  actionResult.runtime.listener_state = "unknown";
  const load = vi.fn()
    .mockResolvedValueOnce(snapshot())
    .mockRejectedValueOnce(new Error("readback unavailable"));
  const action = vi.fn().mockResolvedValue(actionResult);
  render(<ProxyRouteScreen load={load} save={vi.fn()} action={action} />);

  await screen.findByText("Слушает");
  await userEvent.click(screen.getByRole("button", { name: "Проверить маршрут" }));

  expect(await screen.findByText("Операция выполнена, но актуальное состояние маршрута не удалось загрузить. Состояние слушателя не подтверждено.")).toBeInTheDocument();
  expect(screen.getByText("Неизвестно")).toBeInTheDocument();
  expect(screen.getByText("Путь через сервер 44 подтверждён")).toBeInTheDocument();
  expect(screen.getByText("203.0.113.10")).toBeInTheDocument();
  expect(screen.getByText("203.0.113.20")).toBeInTheDocument();
  expect(load).toHaveBeenCalledTimes(2);
  expect(action).toHaveBeenCalledTimes(1);
});

it("reads current listener state after saving the desired profile", async () => {
  let finishReadback!: (value: ProxyRouteState) => void;
  const readback = new Promise<ProxyRouteState>((resolve) => { finishReadback = resolve; });
  const initial = snapshot();
  const saved = snapshot();
  saved.runtime.listener_state = "unknown";
  saved.profile.desired_state = "disabled";
  const freshState = snapshot();
  freshState.profile.desired_state = "disabled";
  const load = vi.fn()
    .mockResolvedValueOnce(initial)
    .mockReturnValueOnce(readback);
  const save = vi.fn().mockResolvedValue(saved);
  render(<ProxyRouteScreen load={load} save={save} action={vi.fn()} />);

  await screen.findByText("Слушает");
  await userEvent.selectOptions(await screen.findByRole("combobox"), "disabled");
  await userEvent.click(screen.getByRole("button", { name: "Сохранить состояние" }));

  expect(await screen.findByText("Неизвестно")).toBeInTheDocument();
  expect(load).toHaveBeenCalledTimes(2);
  expect(screen.getByRole("button", { name: "Включить" })).toBeDisabled();

  finishReadback(freshState);
  expect(await screen.findByText("Слушает")).toBeInTheDocument();
  expect(save).toHaveBeenCalledWith({ ...profile, desired_state: "disabled" });
  expect(screen.getByText("Желаемое состояние сохранено. Чтобы применить его, используйте действие включения или отключения.")).toBeInTheDocument();
});

it("marks runtime unknown and keeps the previous probe as history when a rejected action cannot be read back", async () => {
  const initial = snapshot();
  const load = vi.fn()
    .mockResolvedValueOnce(initial)
    .mockRejectedValueOnce(new Error("status unavailable"));
  const action = vi.fn().mockRejectedValue(new Error("response lost"));
  render(<ProxyRouteScreen load={load} save={vi.fn()} action={action} />);

  await screen.findByText("Слушает");
  await userEvent.click(screen.getByRole("button", { name: "Включить" }));

  expect(await screen.findByText("Не удалось получить ответ операции и актуальное состояние. Выполнена ли операция, неизвестно; сервис и слушатель не подтверждены.")).toBeInTheDocument();
  expect(screen.getAllByText("Неизвестно")).toHaveLength(3);
  expect(screen.queryByText("Работает")).not.toBeInTheDocument();
  expect(screen.getByText("Последние сохранённые данные проверки ниже относятся к прошлому результату; исход операции не подтверждён.")).toBeInTheDocument();
  expect(screen.getByText("Путь через сервер 44 подтверждён")).toBeInTheDocument();
  expect(screen.getByText("203.0.113.10")).toBeInTheDocument();
  expect(screen.getByText("203.0.113.20")).toBeInTheDocument();
  expect(load).toHaveBeenCalledTimes(2);
  expect(action).toHaveBeenCalledTimes(1);
});

it("shows fresh state but leaves a rejected action outcome unconfirmed", async () => {
  const initial = snapshot();
  const current = snapshot({
    state: "unknown",
    checked_at: "2026-10-23T12:30:00Z",
    direct_exit_ip: "192.0.2.10",
    proxy_exit_ip: "192.0.2.20",
    server44_path_verified: null,
    server44_path_reason: "Current path not verified.",
  });
  current.runtime.service_state = "inactive";
  current.runtime.listener_state = "closed";
  const load = vi.fn().mockResolvedValueOnce(initial).mockResolvedValueOnce(current);
  const action = vi.fn().mockRejectedValue(new Error("response lost"));
  render(<ProxyRouteScreen load={load} save={vi.fn()} action={action} />);

  await screen.findByText("Слушает");
  await userEvent.click(screen.getByRole("button", { name: "Отключить" }));

  expect(await screen.findByText("Не удалось подтвердить ответ операции. Показано актуальное состояние маршрута; исход операции остаётся неопределённым.")).toBeInTheDocument();
  expect(screen.getByText("Показано актуальное состояние маршрута. Исход операции остаётся неподтверждённым.")).toBeInTheDocument();
  expect(screen.getByText("Закрыт")).toBeInTheDocument();
  expect(screen.getAllByText("Остановлен")).toHaveLength(2);
  expect(screen.getByText("192.0.2.10")).toBeInTheDocument();
  expect(screen.getByText("192.0.2.20")).toBeInTheDocument();
  expect(screen.queryByText("203.0.113.10")).not.toBeInTheDocument();
  expect(load).toHaveBeenCalledTimes(2);
  expect(action).toHaveBeenCalledTimes(1);
});

it("uses the same recovery readback when saving returns no response", async () => {
  const initial = snapshot();
  const current = snapshot();
  current.runtime.service_state = "inactive";
  current.runtime.listener_state = "closed";
  const load = vi.fn().mockResolvedValueOnce(initial).mockResolvedValueOnce(current);
  const save = vi.fn().mockRejectedValue(new Error("response lost"));
  render(<ProxyRouteScreen load={load} save={save} action={vi.fn()} />);

  await screen.findByText("Слушает");
  await userEvent.selectOptions(screen.getByRole("combobox"), "disabled");
  await userEvent.click(screen.getByRole("button", { name: "Сохранить состояние" }));

  expect(await screen.findByText("Не удалось подтвердить ответ операции. Показано актуальное состояние маршрута; исход операции остаётся неопределённым.")).toBeInTheDocument();
  expect(screen.getByText("Закрыт")).toBeInTheDocument();
  expect(screen.getAllByText("Остановлен")).toHaveLength(2);
  expect(load).toHaveBeenCalledTimes(2);
  expect(save).toHaveBeenCalledTimes(1);
});
