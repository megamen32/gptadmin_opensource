import { afterEach, describe, expect, it, vi } from "vitest";
import {
  deleteClient,
  deleteClientBinding,
  getAccessProfile,
  getAccessProfiles,
  getClients,
  getStoredMcpToken,
  setClientRole,
  issueMcpToken,
  putAccessProfile,
  putClientBinding,
  rotateMcpToken,
  rotateOAuth,
  type AccessProfile,
} from "./api";

const profile: AccessProfile = {
  id: "ops",
  name: "Operations",
  instruction_set_id: "default",
  approval_mode: "read_only",
  access_mode: "readonly",
  allowed_targets: ["hub"],
  allowed_tools: ["discover"],
  external_workspace_refs: [{
    machine_id: "machine-a",
    workspace_path: "/srv/ops",
    startup_document: "AGENTS.md",
    shell_target: "shell:machine-a",
  }],
  version: 2,
  updated_at: "2026-07-17T08:00:00Z",
};

function jsonResponse(value: unknown, headers: HeadersInit = {}): Response {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

afterEach(() => vi.restoreAllMocks());

describe("admin API contracts", () => {
  it("lists and gets profiles with the expected envelope and ETag", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(jsonResponse({ profiles: [profile] }))
      .mockResolvedValueOnce(jsonResponse(profile, { ETag: '"2"' }));

    await expect(getAccessProfiles()).resolves.toEqual([profile]);
    await expect(getAccessProfile("ops")).resolves.toEqual({ value: profile, etag: '"2"' });
    expect(fetchMock).toHaveBeenNthCalledWith(1, "/admin/api/access-profiles", expect.objectContaining({
      credentials: "same-origin",
      headers: { Accept: "application/json" },
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/admin/api/access-profiles/ops", expect.objectContaining({
      credentials: "same-origin",
      headers: { Accept: "application/json" },
    }));
  });

  it("puts a complete profile with JSON and If-Match, preserving the response ETag", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      jsonResponse({ ...profile, version: 3 }, { ETag: '"3"' }),
    );

    await expect(putAccessProfile(profile, '"2"')).resolves.toEqual({ value: { ...profile, version: 3 }, etag: '"3"' });
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/access-profiles/ops", expect.objectContaining({
      method: "PUT",
      credentials: "same-origin",
      body: JSON.stringify({
        id: "ops",
        name: "Operations",
        access_mode: "readonly",
        allowed_targets: ["hub"],
        allowed_tools: ["discover"],
        workspace_refs: profile.external_workspace_refs,
        instruction_set_id: "default",
        approval_mode: "read_only",
      }),
      headers: { Accept: "application/json", "Content-Type": "application/json", "If-Match": '"2"' },
    }));
  });

  it("uses typed client binding and unbinding calls without accepting a secret", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(jsonResponse({ client_id: "jwt-1", profile_id: "ops" }))
      .mockResolvedValueOnce(jsonResponse({ ok: true, unbound: true, client_id: "jwt-1" }))
      .mockResolvedValueOnce(jsonResponse({ ok: true, revoked: true, token_id: "jwt-1" }));

    await expect(putClientBinding("jwt-1", "ops")).resolves.toEqual({ client_id: "jwt-1", profile_id: "ops" });
    await expect(deleteClientBinding("jwt-1")).resolves.toEqual({ ok: true, unbound: true, client_id: "jwt-1" });
    await expect(deleteClient("jwt-1")).resolves.toEqual({ ok: true, revoked: true, token_id: "jwt-1" });
    expect(fetchMock).toHaveBeenNthCalledWith(1, "/admin/api/client-bindings/jwt-1", expect.objectContaining({
      method: "PUT",
      body: JSON.stringify({ profile_id: "ops" }),
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/admin/api/client-bindings/jwt-1", expect.objectContaining({ method: "DELETE" }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/admin/api/clients/jwt-1", expect.objectContaining({ method: "DELETE" }));
  });

  it("lists clients and uses the exact issue, rotate, and OAuth rotation endpoints", async () => {
    const clients = [{
      id: "jwt-1",
      client_id: "codex",
      token_kind: "managed_jwt",
      status: "active",
      access_mode: null,
      profile_id: null,
      scope: null,
      redirect_uris: [],
      issued_at: null,
      created_at: null,
      expires_at: null,
      revoked_at: null,
    }];
    const fetchMock = vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(jsonResponse({ clients }))
      .mockResolvedValueOnce(jsonResponse({ token_id: "jwt-2", access_token: "one-time" }))
      .mockResolvedValueOnce(jsonResponse({ token_id: "jwt-3", access_token: "one-time-2" }))
      .mockResolvedValueOnce(jsonResponse({ ok: true, restart_required: true, message: "restart" }));

    await expect(getClients()).resolves.toEqual(clients);
    await expect(issueMcpToken({ client_id: "new-client", ttl_days: 7, access_mode: "readonly" })).resolves.toEqual({ token_id: "jwt-2", access_token: "one-time" });
    await expect(rotateMcpToken("jwt-1")).resolves.toEqual({ token_id: "jwt-3", access_token: "one-time-2" });
    await expect(rotateOAuth()).resolves.toEqual({ ok: true, restart_required: true, message: "restart" });
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/admin/api/mcp/issue-token", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ client_id: "new-client", ttl_days: 7, access_mode: "readonly" }),
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/admin/api/mcp/tokens/jwt-1/rotate", expect.objectContaining({ method: "POST" }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, "/admin/api/auth/rotate-oauth", expect.objectContaining({ method: "POST" }));
  });

  it("accepts sparse legacy inventory records but rejects secrets in inventory responses", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(jsonResponse({ clients: [{ id: "legacy-ctl", client_id: "legacy-ctl", scope: "legacy transition credential", access_mode: "full" }] }))
      .mockResolvedValueOnce(jsonResponse({ clients: [{ id: "jwt-1", client_id: "codex", access_token: "must-not-be-returned" }] }));

    await expect(getClients()).resolves.toEqual([expect.objectContaining({
      id: "legacy-ctl",
      token_kind: "legacy_ctl",
      status: "legacy",
    })]);
    await expect(getClients()).rejects.toMatchObject({ status: 502 });
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("treats an OAuth inventory record without access_mode as unscoped", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(jsonResponse({ clients: [{
      id: "oauth-1",
      client_id: "oauth-app",
      token_kind: "oauth",
      status: "registered",
      access_mode: "",
      redirect_uris: [],
      created_at: 123,
    }] }));

    await expect(getClients()).resolves.toEqual([expect.objectContaining({
      id: "oauth-1",
      token_kind: "oauth",
      access_mode: null,
    })]);
  });

  it("surfaces HTTP errors, including stale profile writes, without persisting response bodies", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ access_token: "must-not-be-stored" }), { status: 412 }));

    await expect(putAccessProfile(profile, '"1"')).rejects.toMatchObject({ status: 412 });
  });
});

it("preserves the real Go profile fields and does not invent required empty workspaces", async () => {
  const wire = { id: "real", name: "Real", access_mode: "full", approval_mode: "ask_before_write", instruction_set_id: "special", allowed_targets: ["*"], allowed_tools: ["*"], workspace_refs: profile.external_workspace_refs, version: 1 };
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async () => jsonResponse(wire, { ETag: '"1"' }));
  const result = await getAccessProfile("real");
  expect(result.value.approval_mode).toBe("ask_before_write");
  expect(result.value.instruction_set_id).toBe("special");
  expect(result.value.external_workspace_refs).toEqual(profile.external_workspace_refs);
  await putAccessProfile({ ...result.value, external_workspace_refs: [{ machine_id: "", workspace_path: "", startup_document: "AGENTS.md", shell_target: "" }] }, result.etag);
  const request = fetchMock.mock.calls.at(-1)?.[1];
  const saved = JSON.parse(String(request?.body));
  expect(saved.workspace_refs).toEqual([]);
  expect(saved).not.toHaveProperty("external_workspace_refs");
  expect(saved.approval_mode).toBe("ask_before_write");
  expect(saved.instruction_set_id).toBe("special");
});

it("uses the explicit saved-token and role endpoints", async () => {
  const fetchMock = vi.spyOn(globalThis, "fetch")
    .mockResolvedValueOnce(jsonResponse({token_id: "saved", client_id: "operator", access_token: "fixture-value", mcp_url: "https://hub.example/mcp"}))
    .mockResolvedValueOnce(jsonResponse({ok:true}));
  await expect(getStoredMcpToken("saved")).resolves.toMatchObject({token_id:"saved",access_token:"fixture-value"});
  await setClientRole("saved", "admin");
  expect(fetchMock).toHaveBeenNthCalledWith(1, "/admin/api/mcp/tokens/saved/value", expect.objectContaining({credentials:"same-origin"}));
  expect(fetchMock).toHaveBeenNthCalledWith(2, "/admin/api/client-roles/saved", expect.objectContaining({method:"PUT",body:JSON.stringify({role:"admin"})}));
});
