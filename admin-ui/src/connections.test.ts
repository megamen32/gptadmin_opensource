import { expect, it } from "vitest";
import { logicalConnections } from "./connections";
import type { ClientInventoryItem } from "./api";

const row = (id: string, kind: string): ClientInventoryItem => ({ id, client_id: "gptadmin-741d3194016e5b84199e3b12936e6aa9", token_kind: kind, status: "active", access_mode: null, profile_id: null, scope: null, redirect_uris: [], issued_at: 1, created_at: 1, expires_at: null, revoked_at: null });
it("shows one canonical OAuth connection, not eleven retired refresh records", () => {
  const registration = { ...row("gptadmin-741d3194016e5b84199e3b12936e6aa9", "oauth"), role: "client", redirect_uris: ["https://chatgpt.com/connector_platform/oauth/callback"] };
  const refreshes = Array.from({ length: 11 }, (_, i) => ({ ...row(`refresh-${i}`, "oauth_refresh"), status: "revoked", revoked_at: 2, role: "admin" }));
  const result = logicalConnections([...refreshes, registration]);
  expect(result).toHaveLength(1);
  expect(result[0].id).toBe(registration.client_id);
  expect(result[0].role).toBe("client"); // An obsolete token role must not be inherited.
  expect(result[0].credentials).toHaveLength(11);
  expect(result[0].label).toContain("ChatGPT");
  expect(result[0].registered).toBe(true);
});
it("does not turn orphaned refresh history into an editable registration", () => {
  const result = logicalConnections([row("orphan-refresh", "oauth_refresh")]);
  expect(result[0].registered).toBe(false);
  expect(result[0].status).toBe("unregistered");
});
it("keeps independently issued bearer connections distinct even with equal labels", () => {
  expect(logicalConnections([row("bearer-a", "durable"), row("bearer-b", "durable")])).toHaveLength(2);
});
