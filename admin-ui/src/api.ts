export const INSTRUCTION_LIMIT = 16 * 1024;

const instructionSetEndpoint = "/admin/api/instruction-sets/default";
const accessProfilesEndpoint = "/admin/api/access-profiles";
const clientsEndpoint = "/admin/api/clients";

export type InstructionSet = {
  id: string;
  content: string;
  version: string;
  updated_at: string | null;
};

export type AccessMode = "full" | "readonly";

export type ExternalWorkspaceRef = {
  machine_id: string;
  workspace_path: string;
  startup_document: string;
  shell_target: string;
};

export type AccessProfile = {
  id: string;
  name: string;
  access_mode: AccessMode;
  allowed_targets: string[];
  allowed_tools: string[];
  external_workspace_refs: ExternalWorkspaceRef[];
  version: number;
  updated_at: string | null;
};

export type ClientInventoryItem = {
  id: string;
  client_id: string;
  token_kind: string;
  status: string;
  access_mode: AccessMode | null;
  profile_id: string | null;
  scope: string | null;
  redirect_uris: string[];
  issued_at: number | null;
  created_at: number | null;
  expires_at: number | null;
  revoked_at: number | null;
};

export type BindingResponse = {
  profile_id: string;
  client_id?: string;
  token_id?: string;
};

export type TokenResponse = {
  token_id: string;
  access_token: string;
  client_id?: string;
  access_mode?: AccessMode;
  token_type?: string;
  mcp_url?: string;
  replaced_token_id?: string;
};

export type OAuthRotationResponse = {
  ok: boolean;
  restart_required: boolean;
  message: string;
};

export type IssueTokenRequest = {
  client_id: string;
  ttl_days: number;
  access_mode: AccessMode;
};

export type ClientMutationResponse = {
  ok: boolean;
  revoked?: boolean;
  unbound?: boolean;
  token_id?: string;
  client_id?: string;
  profile_id?: string;
};

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

function asRecord(body: unknown, message: string): Record<string, unknown> {
  if (typeof body !== "object" || body === null) {
    throw new ApiError(502, message);
  }
  return body as Record<string, unknown>;
}

function parseInstruction(body: unknown): InstructionSet {
  const value = asRecord(body, "Сервер вернул некорректный профиль инструкций.");

  if (
    typeof value.id !== "string" ||
    typeof value.content !== "string" ||
    typeof value.version !== "string" ||
    (typeof value.updated_at !== "string" && value.updated_at !== null && value.updated_at !== undefined)
  ) {
    throw new ApiError(502, "Сервер вернул неполный профиль инструкций.");
  }

  return {
    id: value.id,
    content: value.content,
    version: value.version,
    updated_at: typeof value.updated_at === "string" ? value.updated_at : null,
  };
}

function parseStringList(value: unknown, field: string): string[] {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.some((item) => typeof item !== "string")) {
    throw new ApiError(502, `Сервер вернул некорректное поле ${field}.`);
  }
  return value;
}

function parseExternalWorkspaceRefs(value: unknown): ExternalWorkspaceRef[] {
  if (value === undefined) return [];
  if (!Array.isArray(value)) {
    throw new ApiError(502, "Сервер вернул некорректные ссылки рабочих пространств.");
  }
  return value.map((item) => {
    const ref = asRecord(item, "Сервер вернул некорректную ссылку рабочего пространства.");
    if (
      typeof ref.machine_id !== "string" ||
      typeof ref.workspace_path !== "string" ||
      typeof ref.startup_document !== "string" ||
      typeof ref.shell_target !== "string"
    ) {
      throw new ApiError(502, "Сервер вернул неполную ссылку рабочего пространства.");
    }
    return {
      machine_id: ref.machine_id,
      workspace_path: ref.workspace_path,
      startup_document: ref.startup_document,
      shell_target: ref.shell_target,
    };
  });
}

function parseAccessProfile(body: unknown): AccessProfile {
  const value = asRecord(body, "Сервер вернул некорректный профиль доступа.");
  if (typeof value.id !== "string" || (value.access_mode !== "full" && value.access_mode !== "readonly")) {
    throw new ApiError(502, "Сервер вернул неполный профиль доступа.");
  }
  if (value.version !== undefined && typeof value.version !== "number") {
    throw new ApiError(502, "Сервер вернул некорректную версию профиля доступа.");
  }
  return {
    id: value.id,
    name: typeof value.name === "string" ? value.name : value.id,
    access_mode: value.access_mode,
    allowed_targets: parseStringList(value.allowed_targets, "allowed_targets"),
    allowed_tools: parseStringList(value.allowed_tools, "allowed_tools"),
    external_workspace_refs: parseExternalWorkspaceRefs(value.external_workspace_refs),
    version: typeof value.version === "number" ? value.version : 0,
    updated_at: typeof value.updated_at === "string" ? value.updated_at : null,
  };
}

function parseProfileEnvelope(body: unknown): AccessProfile[] {
  const value = asRecord(body, "Сервер вернул некорректный список профилей доступа.");
  if (!Array.isArray(value.profiles)) {
    throw new ApiError(502, "Сервер вернул неполный список профилей доступа.");
  }
  return value.profiles.map(parseAccessProfile);
}

function parseClient(body: unknown): ClientInventoryItem {
  const value = asRecord(body, "Сервер вернул некорректную запись клиента.");
  if ("access_token" in value || "client_secret" in value) {
    throw new ApiError(502, "Сервер вернул секрет в инвентаре клиентов.");
  }
  if (typeof value.id !== "string" || typeof value.client_id !== "string") {
    throw new ApiError(502, "Сервер вернул неполную запись клиента.");
  }
  const tokenKind = value.token_kind === undefined
    ? value.id === "legacy-ctl" || value.client_id === "legacy-ctl" ? "legacy_ctl" : "managed_jwt"
    : value.token_kind;
  if (typeof tokenKind !== "string") throw new ApiError(502, "Сервер вернул некорректный тип клиента.");
  // OAuth registrations do not carry a Shell/MCP access policy. Older Hub
  // builds serialized that absence as an empty string; treat it like the
  // contract's null value so one legacy record cannot break the whole client
  // inventory screen.
  const accessMode = value.access_mode === undefined || value.access_mode === null || (tokenKind === "oauth" && value.access_mode === "")
    ? null
    : value.access_mode;
  if (accessMode !== null && accessMode !== "full" && accessMode !== "readonly") {
    throw new ApiError(502, "Сервер вернул некорректный режим доступа клиента.");
  }
  const numeric = (field: string): number | null => {
    const item = value[field];
    if (item === undefined || item === null) return null;
    if (typeof item !== "number") throw new ApiError(502, `Сервер вернул некорректное поле ${field}.`);
    return item;
  };
  const optionalString = (field: string): string | null => {
    const item = value[field];
    if (item === undefined || item === null) return null;
    if (typeof item !== "string") throw new ApiError(502, `Сервер вернул некорректное поле ${field}.`);
    return item;
  };
  const revokedAt = numeric("revoked_at");
  const status = value.status === undefined
    ? revokedAt === null ? tokenKind === "oauth" ? "registered" : tokenKind === "legacy_ctl" ? "legacy" : "active" : "revoked"
    : value.status;
  if (typeof status !== "string") throw new ApiError(502, "Сервер вернул некорректный статус клиента.");
  return {
    id: value.id,
    client_id: value.client_id,
    token_kind: tokenKind,
    status,
    access_mode: accessMode,
    profile_id: optionalString("profile_id"),
    scope: optionalString("scope"),
    redirect_uris: parseStringList(value.redirect_uris, "redirect_uris"),
    issued_at: numeric("issued_at"),
    created_at: numeric("created_at"),
    expires_at: numeric("expires_at"),
    revoked_at: revokedAt,
  };
}

function parseClientEnvelope(body: unknown): ClientInventoryItem[] {
  const value = asRecord(body, "Сервер вернул некорректный список клиентов.");
  if (!Array.isArray(value.clients)) {
    throw new ApiError(502, "Сервер вернул неполный список клиентов.");
  }
  return value.clients.map(parseClient);
}

function parseBinding(body: unknown): BindingResponse {
  const value = asRecord(body, "Сервер вернул некорректную привязку клиента.");
  if (typeof value.profile_id !== "string" || (value.client_id !== undefined && typeof value.client_id !== "string") || (value.token_id !== undefined && typeof value.token_id !== "string")) {
    throw new ApiError(502, "Сервер вернул неполную привязку клиента.");
  }
  return { profile_id: value.profile_id, ...(typeof value.client_id === "string" ? { client_id: value.client_id } : {}), ...(typeof value.token_id === "string" ? { token_id: value.token_id } : {}) };
}

function parseClientMutation(body: unknown): ClientMutationResponse {
  const value = asRecord(body, "Сервер вернул некорректный результат операции с клиентом.");
  if (typeof value.ok !== "boolean") throw new ApiError(502, "Сервер вернул неполный результат операции с клиентом.");
  return {
    ok: value.ok,
    revoked: typeof value.revoked === "boolean" ? value.revoked : undefined,
    unbound: typeof value.unbound === "boolean" ? value.unbound : undefined,
    token_id: typeof value.token_id === "string" ? value.token_id : undefined,
    client_id: typeof value.client_id === "string" ? value.client_id : undefined,
    profile_id: typeof value.profile_id === "string" ? value.profile_id : undefined,
  };
}

function parseToken(body: unknown): TokenResponse {
  const value = asRecord(body, "Сервер вернул некорректный токен.");
  if (typeof value.token_id !== "string" || typeof value.access_token !== "string") {
    throw new ApiError(502, "Сервер не вернул токен для одноразового отображения.");
  }
  if (value.access_mode !== undefined && value.access_mode !== "full" && value.access_mode !== "readonly") {
    throw new ApiError(502, "Сервер вернул некорректный режим доступа токена.");
  }
  return {
    token_id: value.token_id,
    access_token: value.access_token,
    client_id: typeof value.client_id === "string" ? value.client_id : undefined,
    access_mode: value.access_mode,
    token_type: typeof value.token_type === "string" ? value.token_type : undefined,
    mcp_url: typeof value.mcp_url === "string" ? value.mcp_url : undefined,
    replaced_token_id: typeof value.replaced_token_id === "string" ? value.replaced_token_id : undefined,
  };
}

function parseOAuthRotation(body: unknown): OAuthRotationResponse {
  const value = asRecord(body, "Сервер вернул некорректный результат ротации OAuth.");
  if (typeof value.ok !== "boolean" || typeof value.restart_required !== "boolean" || typeof value.message !== "string") {
    throw new ApiError(502, "Сервер вернул неполный результат ротации OAuth.");
  }
  return { ok: value.ok, restart_required: value.restart_required, message: value.message };
}

async function request(path: string, init?: RequestInit): Promise<Response> {
  const response = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers: { Accept: "application/json", ...init?.headers },
  });
  if (!response.ok) {
    throw new ApiError(response.status, "Запрос к Hub не выполнен.");
  }
  return response;
}

async function responseJSON(response: Response): Promise<unknown> {
  try {
    return await response.json();
  } catch {
    throw new ApiError(502, "Сервер вернул некорректный JSON.");
  }
}

function responseETag(response: Response): string {
  const etag = response.headers.get("ETag");
  if (!etag) {
    throw new ApiError(502, "Сервер не вернул версию профиля.");
  }
  return etag;
}

export async function getDefaultInstructionSet(): Promise<{ value: InstructionSet; etag: string }> {
  const response = await request(instructionSetEndpoint);
  return {
    value: parseInstruction(await responseJSON(response)),
    etag: responseETag(response),
  };
}

export async function putDefaultInstructionSet(content: string, etag: string): Promise<{ value: InstructionSet; etag: string }> {
  const response = await request(instructionSetEndpoint, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "If-Match": etag },
    body: JSON.stringify({ content }),
  });
  return {
    value: parseInstruction(await responseJSON(response)),
    etag: responseETag(response),
  };
}

export async function getAccessProfiles(): Promise<AccessProfile[]> {
  const response = await request(accessProfilesEndpoint);
  return parseProfileEnvelope(await responseJSON(response));
}

export async function getAccessProfile(id: string): Promise<{ value: AccessProfile; etag: string }> {
  const response = await request(`${accessProfilesEndpoint}/${encodeURIComponent(id)}`);
  return { value: parseAccessProfile(await responseJSON(response)), etag: responseETag(response) };
}

export async function putAccessProfile(profile: AccessProfile, etag: string): Promise<{ value: AccessProfile; etag: string }> {
  const response = await request(`${accessProfilesEndpoint}/${encodeURIComponent(profile.id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "If-Match": etag },
    body: JSON.stringify({
      id: profile.id,
      name: profile.name,
      access_mode: profile.access_mode,
      allowed_targets: profile.allowed_targets,
      allowed_tools: profile.allowed_tools,
      external_workspace_refs: profile.external_workspace_refs,
    }),
  });
  return { value: parseAccessProfile(await responseJSON(response)), etag: responseETag(response) };
}

export async function getClients(): Promise<ClientInventoryItem[]> {
  const response = await request(clientsEndpoint);
  return parseClientEnvelope(await responseJSON(response));
}

export async function putClientBinding(id: string, profileId: string): Promise<BindingResponse> {
  const response = await request(`/admin/api/client-bindings/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ profile_id: profileId }),
  });
  return parseBinding(await responseJSON(response));
}

export async function deleteClientBinding(id: string): Promise<ClientMutationResponse> {
  const response = await request(`/admin/api/client-bindings/${encodeURIComponent(id)}`, { method: "DELETE" });
  return parseClientMutation(await responseJSON(response));
}

export async function revokeMcpToken(id: string): Promise<ClientMutationResponse> {
  const response = await request(`${clientsEndpoint}/${encodeURIComponent(id)}`, { method: "DELETE" });
  return parseClientMutation(await responseJSON(response));
}

export const deleteClient = revokeMcpToken;

export async function issueMcpToken(input: IssueTokenRequest): Promise<TokenResponse> {
  const response = await request("/admin/api/mcp/issue-token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  return parseToken(await responseJSON(response));
}

export async function rotateMcpToken(id: string): Promise<TokenResponse> {
  const response = await request(`/admin/api/mcp/tokens/${encodeURIComponent(id)}/rotate`, { method: "POST" });
  return parseToken(await responseJSON(response));
}

export async function rotateOAuth(): Promise<OAuthRotationResponse> {
  const response = await request("/admin/api/auth/rotate-oauth", { method: "POST" });
  return parseOAuthRotation(await responseJSON(response));
}

export function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}
