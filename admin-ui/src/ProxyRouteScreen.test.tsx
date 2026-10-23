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
  profile,
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
