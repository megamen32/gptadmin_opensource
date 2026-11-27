import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import ActivityScreen from "./ActivityScreen";

const when = new Date(Date.now() - 20000).toISOString();
const events = [
  { time: when, name: "tool_policy_decision", fields: { client_id: "grepmesh", actor: "grepmesh", tool: "find_paths", target: "shell:node-44", policy_decision: "allow", status: 200, result_reference: "inline" } },
  { time: when, name: "tool_policy_decision", fields: { client_id: "opencode", actor: "opencode", tool: "shell_exec", target: "shell:node-100", policy_decision: "allow", status: 200, trace_id: "trace-other", result_reference: "job-1" } },
];
const jobs = [{ job_id: "job-1", status: "completed", server_id: "shell:node-100", tool_name: "shell_exec", started_at: (Date.now() - 22000) / 1000, completed_at: (Date.now() - 19000) / 1000 }];

function mockActivity() {
  return vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = new URL(String(input), "https://example.test");
    if (url.pathname === "/admin/api/audit") {
      const excluded = url.searchParams.getAll("exclude_client");
      const filtered = events.filter((event) => !excluded.includes(event.fields.client_id));
      return new Response(JSON.stringify({ events: filtered, total: filtered.length, clients: { grepmesh: 1, opencode: 1 } }), { status: 200 });
    }
    if (url.pathname === "/admin/api/jobs") return new Response(JSON.stringify({ recent: jobs }), { status: 200 });
    if (url.pathname === "/admin/api/tasks/job-1") return new Response(JSON.stringify({ resultType: "complete", taskId: "job-1", status: "completed", result: { stdout: "ok" } }), { status: 200 });
    throw new Error("unexpected " + String(input));
  });
}

beforeEach(() => { localStorage.clear(); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); localStorage.clear(); });

it("filters actual clients with checkboxes and keeps hidden preferences", async () => {
  const request = mockActivity();
  const user = userEvent.setup();
  render(<ActivityScreen />);
  expect(await screen.findByRole("button", { name: /find_paths/ })).toBeInTheDocument();
  expect(screen.getByRole("checkbox", { name: /grepmesh/ })).toBeChecked();
  expect(screen.getByRole("checkbox", { name: /opencode/ })).toBeChecked();
  expect(request.mock.calls.some(([url]) => String(url).includes("exclude_client"))).toBe(false);

  await user.click(screen.getByRole("checkbox", { name: /grepmesh/ }));
  await waitFor(() => expect(request.mock.calls.some(([url]) => String(url).includes("exclude_client=grepmesh"))).toBe(true));
  await waitFor(() => expect(screen.queryByRole("button", { name: /find_paths/ })).not.toBeInTheDocument());
  expect(localStorage.getItem("gptadmin:activity:hidden-clients:v1")).toContain("grepmesh");
  expect(screen.getByRole("checkbox", { name: /opencode/ })).toBeChecked();
});

it("shows caller, target, recorded trace and real job details by clicking operation", async () => {
  mockActivity();
  const user = userEvent.setup();
  render(<ActivityScreen />);
  await screen.findByRole("checkbox", { name: /opencode/ });
  await user.click(screen.getByRole("button", { name: "Список" }));
  await user.click(screen.getByRole("button", { name: /shell_exec/ }));
  const detail = await screen.findByRole("complementary", { name: "Детали операции" });
  expect(within(detail).getAllByText("opencode").length).toBeGreaterThan(0);
  expect(within(detail).getByText("shell:node-100")).toBeInTheDocument();
  expect(within(detail).getByText("trace-other")).toBeInTheDocument();
  expect(await within(detail).findByText(/"stdout": "ok"/)).toBeInTheDocument();
  expect(within(detail).getByText("3.0 с")).toBeInTheDocument();
});

it("defaults to compact groups, reveals every grouped call and makes parallel layout opt-in", async () => {
  const crowded = Array.from({ length: 30 }, (_, index) => ({
    time: when, name: "tool_policy_decision",
    fields: { client_id: "zai", tool: "tool_" + index, target: "node-100", status: 200 },
  }));
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) =>
    new Response(JSON.stringify(String(input).includes("/audit?")
      ? { events: crowded, total: crowded.length, clients: { zai: crowded.length } }
      : { recent: [] }), { status: 200 }));
  const user = userEvent.setup();
  const { container } = render(<ActivityScreen />);
  expect(await screen.findByRole("button", { name: "30 операций — zai" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Компактно" })).toHaveAttribute("aria-pressed", "true");
  expect(container.querySelector(".activity-lane")).toHaveStyle({ minHeight: "96px" });
  expect(screen.getByRole("slider", { name: "Положение таймлайна" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Таймлайн вправо" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "30 операций — zai" }));
  const group = screen.getByRole("region", { name: "Операции группы" });
  expect(within(group).getAllByRole("button")).toHaveLength(31);
  await user.click(within(group).getByRole("button", { name: /tool_29/ }));
  expect(screen.getByRole("complementary", { name: "Детали операции" })).toHaveTextContent("tool_29");
  await user.click(screen.getByRole("button", { name: "Параллельные" }));
  expect(container.querySelectorAll(".activity-mark")).toHaveLength(30);
  expect(container.querySelector(".activity-lane")).toHaveStyle({ minHeight: "980px" });
  expect(screen.queryByRole("region", { name: "Операции группы" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Компактно" }));
  expect(container.querySelector(".activity-lane")).toHaveStyle({ minHeight: "96px" });
  await user.click(screen.getByRole("button", { name: "Список" }));
  expect(container.querySelectorAll(".activity-request-row")).toHaveLength(30);
});
