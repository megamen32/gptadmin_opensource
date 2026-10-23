import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import JobsScreen from "./JobsScreen";

afterEach(() => {
  cleanup();
  window.history.replaceState(null, "", "#jobs");
  vi.restoreAllMocks();
});

function response(body: object) {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

it("opens the exact historical task linked from an offline node while listing history by its exact server id", async () => {
  const serverId = "shell:host-a";
  window.history.replaceState(null, "", `#jobs?server_id=${encodeURIComponent(serverId)}&job_id=historical-task-7`);
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = String(input);
    if (path === "/admin/api/jobs?server_id=shell%3Ahost-a&limit=1") {
      return response({ count: 1, recent: [{ job_id: "newer-task-8", server_id: serverId, status: "completed", tool_name: "system_inspect" }] });
    }
    if (path === "/mcp-relay/job/historical-task-7?detail=full") {
      return response({ job_id: "historical-task-7", server_id: serverId, status: "failed", response: { error: "historical result retained" } });
    }
    throw new Error(`Unexpected request ${path}`);
  });
  render(<JobsScreen />);

  expect(await screen.findByRole("heading", { name: "historical-task-7" })).toBeInTheDocument();
  expect(await screen.findByText(/historical result retained/)).toBeInTheDocument();
  expect(screen.getByText("shell:host-a", { selector: "code" })).toBeInTheDocument();
  expect(fetchMock).toHaveBeenCalledWith("/admin/api/jobs?server_id=shell%3Ahost-a&limit=1", expect.objectContaining({ credentials: "same-origin" }));
  expect(fetchMock).toHaveBeenCalledWith("/mcp-relay/job/historical-task-7?detail=full", expect.objectContaining({ credentials: "same-origin" }));
});
