import { afterEach, describe, expect, it, vi } from "vitest";
import {
  deleteClient,
  deleteClientBinding,
  getAccessProfile,
  getAccessProfiles,
  getLatestJobForServer,
  loadProxyRoute,
  saveProxyRoute,
  actOnProxyRoute,
  getClients,
  getStoredMcpToken,
  setClientRole,
  issueMcpToken,
  putAccessProfile,
  putClientBinding,
  rotateMcpToken,
  rotateOAuth,
  sampleServerResources,
  readServerResourceJob,
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

const proxyRoute = {
  profile: {
    schema: "gptadmin.proxy-route/v1",
    id: "relay02-server44-de-http",
    consumer: "relay02",
    exit: "node-44-de-http",
    listener: { bind_address: "127.0.0.1", port: 24836 },
    ssh_identity_ref: "ssh-config:relay02",
    desired_state: "disabled",
  },
  runtime: { profile_saved: false, service_state: "inactive", listener_state: "closed", last_probe: null },
} as const;

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

  it("preserves an explicit empty root list and keeps an omitted ACL side absent", async () => {
    const readOnlyAcl: AccessProfile = { ...profile, read_roots: [] };
    const responseProfile = { ...readOnlyAcl, version: 3 };
    const fetchMock = vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(jsonResponse(readOnlyAcl, { ETag: '"2"' }))
      .mockResolvedValueOnce(jsonResponse(responseProfile, { ETag: '"3"' }));

    const loaded = await getAccessProfile("ops");
    expect(loaded.value).toEqual(readOnlyAcl);
    expect(Object.hasOwn(loaded.value, "read_roots")).toBe(true);
    expect(Object.hasOwn(loaded.value, "write_roots")).toBe(false);
    await expect(putAccessProfile(loaded.value, loaded.etag)).resolves.toEqual({ value: responseProfile, etag: '"3"' });

    const putBody = JSON.parse(String(fetchMock.mock.calls[1][1]?.body)) as Record<string, unknown>;
    expect(putBody.read_roots).toEqual([]);
    expect(putBody).not.toHaveProperty("write_roots");
  });

  it("samples one exact shell node and validates the resource summary envelope", async () => {
    const summary = {
      server_id: "shell:host-a",
      status: "available",
      sampled_at: "2026-10-23T00:00:00Z",
      cpu: { status: "available", cores: 8, utilization_percent: 12.5, sample_interval_ms: 250 },
      memory: { status: "available", total_bytes: 1000, available_bytes: 400, used_bytes: 600 },
      root_filesystem: { status: "available", mount: "/", total_bytes: 5000, used_bytes: 2000, free_bytes: 3000 },
    };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(jsonResponse(summary));

    await expect(sampleServerResources("shell:host-a")).resolves.toEqual(summary);
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/servers/resources", expect.objectContaining({
      method: "POST",
      credentials: "same-origin",
      body: JSON.stringify({ server_id: "shell:host-a" }),
    }));
  });

  it("reads a pending sample through its existing job id without creating a second sample", async () => {
    const pending = { server_id: "shell:host-a", status: "pending", job_id: "resource-job" };
    const summary = {
      status: "partial",
      sampled_at: "2026-10-23T00:00:01Z",
      cpu: { status: "available", cores: 4, utilization_percent: 5, sample_interval_ms: 250 },
      memory: { status: "unavailable", error: "unsupported" },
      root_filesystem: { status: "available", mount: "/", total_bytes: 100, used_bytes: 40, free_bytes: 60 },
    };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(jsonResponse(pending)).mockResolvedValueOnce(jsonResponse({
      server_id: "shell:host-a", status: "running", job_id: "resource-job",
    })).mockResolvedValueOnce(jsonResponse({
      server_id: "shell:host-a",
      status: "completed",
      response: { structuredContent: { server: "host-a", result: summary } },
    }));

    await expect(sampleServerResources("shell:host-a")).resolves.toEqual(pending);
    await expect(readServerResourceJob("resource-job", "shell:host-a")).resolves.toEqual(pending);
    await expect(readServerResourceJob("resource-job", "shell:host-a")).resolves.toMatchObject(summary);
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/mcp-relay/job/resource-job?detail=full", expect.objectContaining({ credentials: "same-origin" }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/mcp-relay/job/resource-job?detail=full", expect.objectContaining({ credentials: "same-origin" }));
    expect(fetchMock.mock.calls.filter(([input]) => input === "/admin/api/servers/resources")).toHaveLength(1);
  });

  it("finds the latest historical task by exact shell server_id", async () => {
    const latest = { job_id: "job-9", server_id: "shell:host-a", status: "failed", tool_name: "system_inspect" };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(jsonResponse({ count: 1, recent: [latest] }));

    await expect(getLatestJobForServer("shell:host-a")).resolves.toEqual(latest);
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/jobs?server_id=shell%3Ahost-a&limit=1", expect.objectContaining({ credentials: "same-origin" }));
  });

  it("loads, saves and invokes only the fixed persistent proxy-route contract", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(jsonResponse(proxyRoute))
      .mockResolvedValueOnce(jsonResponse({ ...proxyRoute, profile: { ...proxyRoute.profile, desired_state: "enabled" }, runtime: { ...proxyRoute.runtime, profile_saved: true } }))
      .mockResolvedValueOnce(jsonResponse({ ...proxyRoute, runtime: { ...proxyRoute.runtime, profile_saved: true, service_state: "active", listener_state: "listening" } }));

    await expect(loadProxyRoute()).resolves.toEqual(proxyRoute);
    await expect(saveProxyRoute({ ...proxyRoute.profile, desired_state: "enabled" })).resolves.toMatchObject({ profile: { desired_state: "enabled" }, runtime: { profile_saved: true } });
    await expect(actOnProxyRoute("enable")).resolves.toMatchObject({ runtime: { profile_saved: true, service_state: "active", listener_state: "listening" } });

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/admin/api/proxy-route", expect.objectContaining({ credentials: "same-origin" }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/admin/api/proxy-route", expect.objectContaining({
      method: "PUT",
      body: JSON.stringify({ profile: { ...proxyRoute.profile, desired_state: "enabled" } }),
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/admin/api/proxy-route/enable", expect.objectContaining({ method: "POST", body: "{}" }));
  });

  it("rejects a proxy profile outside the fixed route", async () => {
    const invalid = { ...proxyRoute, profile: { ...proxyRoute.profile, listener: { bind_address: "0.0.0.0", port: 24836 } } };
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(jsonResponse(invalid));
    await expect(loadProxyRoute()).rejects.toThrow("proxy profile вне разрешённой конфигурации");
  });

  it("preserves unknown proxy verification and its reason without coercing it to false", async () => {
    const state = {
      ...proxyRoute,
      runtime: {
        profile_saved: true,
        service_state: "unknown",
        listener_state: "unknown",
        last_probe: {
          state: "unknown",
          checked_at: null,
          direct_exit_ip: null,
          proxy_exit_ip: null,
          server44_path_verified: null,
          server44_path_reason: "ssh_readback_unavailable",
        },
      },
    };
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(jsonResponse(state));

    await expect(loadProxyRoute()).resolves.toEqual(state);
  });

  it("requires the backend to report whether the route profile is persisted", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(jsonResponse({
      ...proxyRoute,
      runtime: { service_state: "inactive", listener_state: "closed", last_probe: null },
    }));

    await expect(loadProxyRoute()).rejects.toThrow("некорректный статус proxy-маршрута");
  });

  it("restores only a matching weak profile version ETag before If-Match", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      jsonResponse(profile, { ETag: 'W/"2"' }),
    ).mockResolvedValueOnce(
      jsonResponse({ ...profile, version: 3 }, { ETag: 'W/"3"' }),
    ).mockResolvedValueOnce(
      new Response(JSON.stringify({ detail: "access profile version does not match" }), { status: 412 }),
    );

    const loaded = await getAccessProfile("ops");
    expect(loaded.etag).toBe('W/"2"');
    await putAccessProfile(loaded.value, loaded.etag);
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/admin/api/access-profiles/ops", expect.objectContaining({
      headers: expect.objectContaining({ "If-Match": '"2"' }),
    }));

    await expect(putAccessProfile({ ...profile, version: 3 }, 'W/"2"')).rejects.toMatchObject({ status: 412 });
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/admin/api/access-profiles/ops", expect.objectContaining({
      headers: expect.objectContaining({ "If-Match": 'W/"2"' }),
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

  it("surfaces a safe validation detail from a failed profile write", async () => {
    const invalidDraft = {
      ...profile,
      external_workspace_refs: profile.external_workspace_refs.map((ref) => ({ ...ref, shell_target: "" })),
    };
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ detail: "workspace_refs.shell_target must not be empty" }), {
      status: 400,
      headers: { "Content-Type": "application/json" },
    }));

    await expect(putAccessProfile(invalidDraft, '"1"')).rejects.toMatchObject({
      status: 400,
      message: "workspace_refs.shell_target must not be empty",
    });
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body)).workspace_refs[0]).toMatchObject({
      machine_id: "machine-a",
      workspace_path: "/srv/ops",
      shell_target: "",
    });
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
