import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import OverviewScreen from "./OverviewScreen";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

function response(body: object) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

// The shape reported by three testers: 38 online, 12 stale, 3 failed out of 53 nodes.
const SERVERS = [
  ...Array.from({ length: 38 }, (_, index) => ({ server_id: `shell:node-${index}`, name: `Shell: node-${index}`, status: "online", kind: "virtual_shell", last_seen: Date.now() / 1000, meta: {} })),
  ...Array.from({ length: 12 }, (_, index) => ({ server_id: `shell:stale-${index}`, name: `Shell: stale-${index}`, status: "stale", kind: "virtual_shell", last_seen: 1000, meta: {} })),
  ...["BrowserOS", "UserIO", "S21NetworkAgentDevice"].map((ref) => ({
    server_id: `mcp:shell:admin-node-100:${ref}`,
    name: ref === "UserIO" ? ref : `${ref} on admin-node-100`,
    status: "failed",
    kind: "child_mcp",
    last_seen: 1000,
    meta: { child_ref: ref },
  })),
];

async function renderOverview() {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    if (String(input).startsWith("/admin/api/overview")) {
      return response({ servers: SERVERS, server_counts: { online: 38, offline: 0, stale: 12 }, client_count: 1, jobs: { recent: [], queued: [], background: [] } });
    }
    throw new Error(`Unexpected request ${String(input)}`);
  });
  render(<OverviewScreen />);
  const tile = await screen.findByText(/работают/);
  return tile.closest("a") as HTMLElement;
}

it("keeps the overview summary consistent with the node list, failed included", async () => {
  const tileRow = await renderOverview();
  expect(within(tileRow).getByText("38 работают")).toBeTruthy();
  // 12 stale + 3 failed: the summary must cover every non-online node, not just stale.
  expect(within(tileRow).getByText("15 требуют внимания, 3 с ошибкой")).toBeTruthy();
});

it("lists every problem node and localises the failed status", async () => {
  await renderOverview();
  const panel = (await screen.findByText("ТРЕБУЕТ ВНИМАНИЯ")).closest("article") as HTMLElement;
  // No slice(0, 6): the rows match the count reported in the summary.
  const rows = within(panel).getAllByRole("link");
  expect(rows).toHaveLength(15);
  expect(rows[0].textContent).toContain("MCP-компонент: BrowserOS");
  expect(rows[0].textContent).toContain("Ошибка");
});