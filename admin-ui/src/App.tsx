import { useEffect, useRef, useState } from "react";
import {
  ApiError,
  byteLength,
  getAccessProfiles,
  getClients,
  getAccessProfile,
  getDefaultInstructionSet,
  INSTRUCTION_LIMIT,
  issueMcpToken,
  putClientBinding,
  putAccessProfile,
  putDefaultInstructionSet,
  revokeMcpToken,
  rotateOAuth,
  rotateMcpToken,
  deleteClientBinding,
  type ClientInventoryItem,
  type TokenResponse,
  type AccessMode,
  type AccessProfile,
  type ExternalWorkspaceRef,
  type InstructionSet,
} from "./api";
import "./styles.css";

type View = "instructions" | "profiles" | "clients" | "auth";
type LoadState = "loading" | "ready" | "empty" | "error" | "stale";

const navigation: Array<{ id: View; label: string; href: string }> = [
  { id: "instructions", label: "Инструкции", href: "#instructions" },
  { id: "profiles", label: "Профили", href: "#profiles" },
  { id: "clients", label: "Клиенты", href: "#clients" },
  { id: "auth", label: "Авторизация", href: "#auth" },
];

const emptyWorkspace = (): ExternalWorkspaceRef => ({
  machine_id: "",
  workspace_path: "",
  startup_document: "AGENTS.md",
  shell_target: "",
});

const emptyProfile = (): AccessProfile => ({
  id: "",
  name: "",
  access_mode: "readonly",
  allowed_targets: [],
  allowed_tools: [],
  external_workspace_refs: [emptyWorkspace()],
  version: 0,
  updated_at: null,
});

function formatBytes(bytes: number): string {
  return `${bytes.toLocaleString("ru-RU")} / 16 384 байт`;
}

function formatUpdated(value: string | null | undefined): string {
  if (value === null) return "Встроенная версия";
  if (!value) return "Нет данных";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Некорректная дата";
  return new Intl.DateTimeFormat("ru-RU", { dateStyle: "medium", timeStyle: "short" }).format(date);
}

function stateLabel(state: LoadState): string {
  if (state === "loading") return "Загрузка";
  if (state === "ready") return "Данные загружены";
  if (state === "empty") return "Нет данных";
  if (state === "stale") return "Нужна актуальная версия";
  return "Ошибка загрузки";
}

function listValue(values: string[]): string {
  return values.join(", ");
}

function parseList(value: string): string[] {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function updateWorkspace(
  workspaces: ExternalWorkspaceRef[],
  index: number,
  field: keyof ExternalWorkspaceRef,
  value: string,
): ExternalWorkspaceRef[] {
  return workspaces.map((workspace, workspaceIndex) => workspaceIndex === index ? { ...workspace, [field]: value } : workspace);
}

function InstructionsScreen() {
  const [loadState, setLoadState] = useState<LoadState>("loading");
  const [profile, setProfile] = useState<InstructionSet | null>(null);
  const [draft, setDraft] = useState("");
  const [etag, setEtag] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [publishing, setPublishing] = useState(false);

  async function load(): Promise<void> {
    setLoadState("loading");
    setMessage(null);
    try {
      const result = await getDefaultInstructionSet();
      setProfile(result.value);
      setDraft(result.value.content);
      setEtag(result.etag);
      setLoadState("ready");
    } catch (error) {
      setProfile(null);
      setEtag(null);
      setLoadState(error instanceof ApiError && error.status === 404 ? "empty" : "error");
      setMessage(error instanceof Error ? error.message : "Не удалось загрузить инструкции.");
    }
  }

  useEffect(() => {
    void load();
  }, []);

  const bytes = byteLength(draft);
  const overLimit = bytes > INSTRUCTION_LIMIT;
  const dirty = profile !== null && draft !== profile.content;

  async function publish(): Promise<void> {
    if (!etag || overLimit || publishing || !dirty) return;
    setPublishing(true);
    setMessage(null);
    try {
      const result = await putDefaultInstructionSet(draft, etag);
      setProfile(result.value);
      setDraft(result.value.content);
      setEtag(result.etag);
      setMessage("Опубликовано только что");
    } catch (error) {
      if (error instanceof ApiError && error.status === 412) {
        setMessage("Инструкции изменились на сервере. Загрузите актуальную версию перед публикацией.");
      } else {
        setMessage(error instanceof Error ? error.message : "Не удалось опубликовать изменения.");
      }
    } finally {
      setPublishing(false);
    }
  }

  return (
    <>
      <header className="topbar">
        <div><span className="eyebrow">РАБОЧИЙ КОНТЕКСТ</span><h1>Инструкции</h1></div>
      </header>
      <div className="content-wrap">
        <section className="intro">
          <div>
            <p className="section-kicker">DEFAULT INSTRUCTION SET / 01</p>
            <h2>Правила для MCP-клиентов</h2>
            <p className="lede">Опубликованный текст становится рабочим контекстом подключённых MCP-клиентов. Права доступа Hub остаются отдельной границей.</p>
          </div>
          <div className={`data-badge state-${loadState}`} role="status"><span className="state-dot" aria-hidden="true" />{stateLabel(loadState)}</div>
        </section>
        <section className="profile-grid">
          <div className="editor-card card">
            <div className="card-heading"><div><h3>Default</h3><p className="muted">{profile ? `Версия ${profile.version.slice(0, 12)}` : stateLabel(loadState)}</p></div><span className={`chip ${dirty ? "chip-dirty" : ""}`}>{dirty ? "Не опубликовано" : loadState === "ready" ? "Загружено" : "Нет статуса"}</span></div>
            {loadState === "loading" && <div className="state-panel" role="status"><span className="loader" aria-hidden="true" />Загрузка профиля…</div>}
            {loadState === "error" && <div className="state-panel state-error" role="alert"><strong>Не удалось загрузить профиль</strong><span>{message}</span><button className="button secondary" type="button" onClick={() => void load()}>Повторить</button></div>}
            {loadState === "empty" && <div className="state-panel"><strong>Default не найден</strong><span>Hub не вернул профиль инструкций.</span><button className="button secondary" type="button" onClick={() => void load()}>Повторить</button></div>}
            {loadState === "ready" && <>
              <label className="editor-label" htmlFor="instructions">Текст инструкций</label>
              <textarea id="instructions" value={draft} onChange={(event) => setDraft(event.target.value)} aria-describedby="byte-count editor-help" spellCheck="false" />
              <div className="editor-footer"><span id="byte-count" className={overLimit ? "byte-count warning" : "byte-count"}>{formatBytes(bytes)}{overLimit && <span> · Превышен лимит 16 KiB</span>}</span><span id="editor-help" className="editor-hint">Markdown поддерживается</span></div>
              <div className="action-row"><div className="publish-status" aria-live="polite">{message && <span className={message.includes("изменились") || message.includes("Не удалось") ? "warning-text" : "success-text"}>{message}</span>}</div><button className="button primary" type="button" onClick={() => void publish()} disabled={!dirty || overLimit || !etag || publishing}>{publishing ? "Публикуем…" : "Опубликовать"}</button></div>
              {message?.includes("изменились") && <button className="reload-link" type="button" onClick={() => void load()}>Загрузить актуальную версию</button>}
            </>}
          </div>
          <aside className="details-column" aria-label="Сведения об инструкциях"><div className="card detail-card"><p className="section-kicker">НАБОР ИНСТРУКЦИЙ</p><h3>Default</h3><dl><div><dt>Ответ API</dt><dd>{loadState === "ready" ? "Получен" : loadState === "loading" ? "Ожидание" : "Не получен"}</dd></div><div><dt>Версия</dt><dd>{profile?.version.slice(0, 12) ?? "Нет данных"}</dd></div><div><dt>Обновлено</dt><dd>{formatUpdated(profile?.updated_at)}</dd></div><div><dt>Размер черновика</dt><dd>{loadState === "ready" ? formatBytes(bytes) : "Нет данных"}</dd></div></dl></div><div className="note-card"><span className="note-icon" aria-hidden="true">i</span><p><strong>Безопасная граница</strong><br />Инструкции не заменяют права доступа и подтверждения Hub.</p></div></aside>
        </section>
      </div>
    </>
  );
}

function ProfileForm({
  draft,
  creating,
  state,
  message,
  saved,
  onChange,
  onSave,
  onReload,
  onCancel,
  onAddWorkspace,
  onRemoveWorkspace,
}: {
  draft: AccessProfile;
  creating: boolean;
  state: LoadState;
  message: string | null;
  saved: AccessProfile | null;
  onChange: (profile: AccessProfile) => void;
  onSave: () => void;
  onReload: () => void;
  onCancel: () => void;
  onAddWorkspace: () => void;
  onRemoveWorkspace: (index: number) => void;
}) {
  const dirty = saved === null || JSON.stringify(draft) !== JSON.stringify(saved);
  const canSave = Boolean(draft.id.trim() && draft.name.trim()) && dirty && state !== "loading" && state !== "stale";
  return (
    <div className="card profile-editor-card">
      <div className="card-heading"><div><p className="section-kicker">{creating ? "NEW PROFILE" : "ACCESS PROFILE"}</p><h3>{creating ? "Новый профиль" : draft.name || draft.id}</h3></div><span className={`chip ${dirty ? "chip-dirty" : ""}`}>{dirty ? "Есть изменения" : "Синхронизировано"}</span></div>
      {state === "loading" && <div className="state-panel compact" role="status"><span className="loader" aria-hidden="true" />Загрузка профиля…</div>}
      {state === "stale" && <div className="stale-callout" role="alert"><strong>Профиль устарел</strong><span>Другой оператор уже изменил эту версию. Сначала загрузите актуальные данные.</span><button className="reload-link" type="button" onClick={onReload}>Загрузить актуальную версию</button></div>}
      {state === "error" && <div className="state-panel state-error compact" role="alert"><strong>Не удалось загрузить профиль</strong><span>{message}</span><button className="button secondary" type="button" onClick={onReload}>Повторить</button></div>}
      {(state === "ready" || state === "stale") && <form className="profile-form" onSubmit={(event) => { event.preventDefault(); onSave(); }}>
        <div className="form-grid">
          <label>Идентификатор профиля<input value={draft.id} disabled={!creating} onChange={(event) => onChange({ ...draft, id: event.target.value })} /></label>
          <label>Название профиля<input value={draft.name} onChange={(event) => onChange({ ...draft, name: event.target.value })} /></label>
          <label>Режим доступа<select value={draft.access_mode} onChange={(event) => onChange({ ...draft, access_mode: event.target.value as AccessMode })}><option value="readonly">Только чтение</option><option value="full">Полный доступ</option></select></label>
          <label>Набор инструкций<select value="default" disabled><option value="default">Default</option></select><small>Закреплённый набор инструкций Hub</small></label>
        </div>
        <label>Разрешённые цели<textarea className="short-textarea" value={listValue(draft.allowed_targets)} onChange={(event) => onChange({ ...draft, allowed_targets: parseList(event.target.value) })} placeholder="hub, shell:machine-a" /></label>
        <label>Разрешённые инструменты<textarea className="short-textarea" value={listValue(draft.allowed_tools)} onChange={(event) => onChange({ ...draft, allowed_tools: parseList(event.target.value) })} placeholder="discover, system_inspect" /></label>
        <fieldset className="workspace-fieldset"><legend>Внешние рабочие пространства</legend><p className="field-help">Ссылки сохраняются как метаданные. Содержимое workspace не копируется в Hub.</p>{draft.external_workspace_refs.map((workspace, index) => <div className="workspace-ref" key={`${index}-${workspace.machine_id}`}><div className="workspace-ref-heading"><strong>Рабочее пространство {index + 1}</strong>{draft.external_workspace_refs.length > 1 && <button className="text-button danger-text" type="button" onClick={() => onRemoveWorkspace(index)}>Удалить</button>}</div><div className="form-grid workspace-grid"><label>Machine ID<input aria-label={`Рабочее пространство ${index + 1}: machine id`} value={workspace.machine_id} onChange={(event) => onChange({ ...draft, external_workspace_refs: updateWorkspace(draft.external_workspace_refs, index, "machine_id", event.target.value) })} /></label><label>Путь<input aria-label={`Рабочее пространство ${index + 1}: путь`} value={workspace.workspace_path} onChange={(event) => onChange({ ...draft, external_workspace_refs: updateWorkspace(draft.external_workspace_refs, index, "workspace_path", event.target.value) })} /></label><label>Startup document<input aria-label={`Рабочее пространство ${index + 1}: startup document`} value={workspace.startup_document} onChange={(event) => onChange({ ...draft, external_workspace_refs: updateWorkspace(draft.external_workspace_refs, index, "startup_document", event.target.value) })} /></label><label>Shell target<input aria-label={`Рабочее пространство ${index + 1}: shell target`} value={workspace.shell_target} onChange={(event) => onChange({ ...draft, external_workspace_refs: updateWorkspace(draft.external_workspace_refs, index, "shell_target", event.target.value) })} /></label></div></div>)}<button className="button secondary add-workspace" type="button" onClick={onAddWorkspace}>Добавить рабочее пространство</button></fieldset>
        <div className="action-row profile-actions"><div className="publish-status" aria-live="polite">{message && <span className={message.includes("Не удалось") ? "warning-text" : "success-text"}>{message}</span>}</div><div className="button-row">{creating && <button className="button secondary" type="button" onClick={onCancel}>Отмена</button>}<button className="button primary" type="submit" disabled={!canSave}>{creating ? "Создать профиль" : "Сохранить профиль"}</button></div></div>
      </form>}
    </div>
  );
}

function ProfilesScreen() {
  const [listState, setListState] = useState<LoadState>("loading");
  const [profiles, setProfiles] = useState<AccessProfile[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [draft, setDraft] = useState<AccessProfile | null>(null);
  const [saved, setSaved] = useState<AccessProfile | null>(null);
  const [etag, setEtag] = useState<string | null>(null);
  const [profileState, setProfileState] = useState<LoadState>("empty");
  const [message, setMessage] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const requestNumber = useRef(0);

  async function loadProfiles(): Promise<void> {
    setListState("loading");
    try {
      const result = await getAccessProfiles();
      setProfiles(result);
      setListState(result.length ? "ready" : "empty");
      setSelectedId((current) => current && result.some((profile) => profile.id === current) ? current : result[0]?.id ?? null);
      if (!result.length) {
        setDraft(null);
        setSaved(null);
        setProfileState("empty");
      }
    } catch (error) {
      setListState("error");
      setProfileState("error");
      setMessage(error instanceof Error ? error.message : "Не удалось загрузить профили.");
    }
  }

  async function loadProfile(id: string): Promise<void> {
    const currentRequest = ++requestNumber.current;
    setProfileState("loading");
    setMessage(null);
    try {
      const result = await getAccessProfile(id);
      if (currentRequest !== requestNumber.current) return;
      setDraft(result.value);
      setSaved(result.value);
      setEtag(result.etag);
      setProfileState("ready");
    } catch (error) {
      if (currentRequest !== requestNumber.current) return;
      setDraft(null);
      setSaved(null);
      setEtag(null);
      setProfileState("error");
      setMessage(error instanceof Error ? error.message : "Не удалось загрузить профиль.");
    }
  }

  useEffect(() => { void loadProfiles(); }, []);
  useEffect(() => { if (selectedId && !creating) void loadProfile(selectedId); }, [selectedId, creating]);

  function startCreate(): void {
    requestNumber.current += 1;
    setCreating(true);
    setSelectedId(null);
    setDraft(emptyProfile());
    setSaved(null);
    setEtag("*");
    setProfileState("ready");
    setMessage(null);
  }

  function selectProfile(id: string): void {
    setCreating(false);
    setSelectedId(id);
  }

  async function saveProfile(): Promise<void> {
    if (!draft || !etag || !draft.id.trim() || !draft.name.trim() || profileState === "stale") return;
    setProfileState("loading");
    setMessage(null);
    try {
      const result = await putAccessProfile(draft, etag);
      setDraft(result.value);
      setSaved(result.value);
      setEtag(result.etag);
      setProfiles((current) => current.some((profile) => profile.id === result.value.id) ? current.map((profile) => profile.id === result.value.id ? result.value : profile) : [...current, result.value]);
      setSelectedId(result.value.id);
      setCreating(false);
      setProfileState("ready");
      setListState("ready");
      setMessage("Профиль сохранён");
    } catch (error) {
      setProfileState(error instanceof ApiError && error.status === 412 ? "stale" : "error");
      setMessage(error instanceof Error ? error.message : "Не удалось сохранить профиль.");
    }
  }

  return (
    <>
      <header className="topbar"><div><span className="eyebrow">ACCESS POLICY / 02</span><h1>Профили доступа</h1></div><button className="button primary topbar-action" type="button" onClick={startCreate}>Новый профиль</button></header>
      <div className="content-wrap">
        <section className="intro"><div><p className="section-kicker">ПРОФИЛИ / WORKSPACE BOUNDARIES</p><h2>Кому и что разрешено</h2><p className="lede">Профиль объединяет режим доступа, разрешённые цели и инструменты с ссылками на внешние рабочие пространства.</p></div><div className={`data-badge state-${listState}`} role="status"><span className="state-dot" aria-hidden="true" />{listState === "loading" ? "Загрузка профилей" : stateLabel(listState)}</div></section>
        {listState === "loading" && <div className="state-panel card standalone-state" role="status"><span className="loader" aria-hidden="true" />Загрузка профилей</div>}
        {listState === "error" && <div className="state-panel card standalone-state state-error" role="alert"><strong>Не удалось загрузить профили</strong><span>{message}</span><button className="button secondary" type="button" onClick={() => void loadProfiles()}>Повторить</button></div>}
        {listState === "empty" && !creating && <div className="empty-card card"><span className="empty-mark" aria-hidden="true">+</span><h3>Профилей пока нет</h3><p>Создайте первый профиль, чтобы описать границу доступа MCP-клиента.</p><button className="button primary" type="button" onClick={startCreate}>Создать профиль</button></div>}
        {(listState === "ready" || creating) && <section className="profiles-layout"><aside className="profile-list card" aria-label="Список профилей"><div className="list-heading"><div><p className="section-kicker">СОХРАНЁННЫЕ</p><h3>Профили</h3></div><button className="text-button" type="button" onClick={startCreate}>+ Новый</button></div>{profiles.length === 0 && creating && <p className="muted list-empty">Список появится после сохранения.</p>}{profiles.map((profile) => <button className={`profile-list-item ${selectedId === profile.id ? "selected" : ""}`} type="button" key={profile.id} onClick={() => selectProfile(profile.id)} aria-current={selectedId === profile.id ? "true" : undefined}><span><strong>{profile.name || profile.id}</strong><small>{profile.id} · {profile.access_mode === "readonly" ? "Только чтение" : "Полный доступ"}</small></span><span className="list-arrow" aria-hidden="true">↗</span></button>)}</aside><div className="profile-detail">{draft ? <ProfileForm draft={draft} creating={creating} state={profileState} message={message} saved={saved} onChange={setDraft} onSave={() => void saveProfile()} onReload={() => selectedId ? void loadProfile(selectedId) : void loadProfiles()} onCancel={() => { setCreating(false); setSelectedId(profiles[0]?.id ?? null); }} onAddWorkspace={() => setDraft({ ...draft, external_workspace_refs: [...draft.external_workspace_refs, emptyWorkspace()] })} onRemoveWorkspace={(index) => setDraft({ ...draft, external_workspace_refs: draft.external_workspace_refs.filter((_, workspaceIndex) => workspaceIndex !== index) })} /> : <div className="state-panel card standalone-state" role="status">Выберите профиль из списка.</div>}</div></section>}
      </div>
    </>
  );
}

function clientKindLabel(kind: string): string {
  if (kind === "managed_jwt") return "Managed JWT";
  if (kind === "legacy_ctl") return "Legacy CTL";
  if (kind === "oauth") return "OAuth";
  return kind;
}

function clientStatusLabel(status: string): string {
  if (status === "active") return "Активен";
  if (status === "revoked") return "Отозван";
  return status;
}

function ClientsScreen() {
  const [loadState, setLoadState] = useState<LoadState>("loading");
  const [clients, setClients] = useState<ClientInventoryItem[]>([]);
  const [profiles, setProfiles] = useState<AccessProfile[]>([]);
  const [clientId, setClientId] = useState("managed-client");
  const [ttlDays, setTtlDays] = useState("7");
  const [accessMode, setAccessMode] = useState<AccessMode>("readonly");
  const [token, setToken] = useState<TokenResponse | null>(null);
  const [selectedClientId, setSelectedClientId] = useState<string | null>(null);
  const [selectedProfileId, setSelectedProfileId] = useState("");
  const [message, setMessage] = useState<string | null>(null);
  const [issuing, setIssuing] = useState(false);
  const [mutating, setMutating] = useState(false);

  async function load(): Promise<void> {
    setLoadState("loading");
    setMessage(null);
    try {
      const [clientResult, profileResult] = await Promise.all([getClients(), getAccessProfiles()]);
      setClients(clientResult);
      setProfiles(profileResult);
      setSelectedClientId((current) => current && clientResult.some((client) => client.id === current) ? current : clientResult[0]?.id ?? null);
      setSelectedProfileId((current) => current || clientResult[0]?.profile_id || "");
      setLoadState(clientResult.length ? "ready" : "empty");
    } catch (error) {
      setLoadState("error");
      setMessage(error instanceof Error ? error.message : "Не удалось загрузить клиентов.");
    }
  }

  useEffect(() => { void load(); }, []);

  async function issueToken(): Promise<void> {
    if (!clientId.trim() || issuing) return;
    setIssuing(true);
    setMessage(null);
    try {
      const result = await issueMcpToken({ client_id: clientId.trim(), ttl_days: Math.max(1, Number(ttlDays) || 7), access_mode: accessMode });
      setToken(result);
      setMessage(null);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Не удалось выдать managed token.");
    } finally {
      setIssuing(false);
    }
  }

  async function copyToken(): Promise<void> {
    if (!token) return;
    await navigator.clipboard?.writeText(token.access_token);
    setMessage("Bearer скопирован. Храните его как пароль.");
  }

  const profileName = (profileId: string | null): string => profiles.find((profile) => profile.id === profileId)?.name ?? profileId ?? "Не привязан";
  const selectedClient = clients.find((client) => client.id === selectedClientId) ?? null;
  const supportsManagedActions = selectedClient?.token_kind === "managed_jwt";

  function selectClient(client: ClientInventoryItem): void {
    setSelectedClientId(client.id);
    setSelectedProfileId(client.profile_id ?? "");
    setMessage(null);
  }

  async function bindProfile(): Promise<void> {
    if (!selectedClient || !selectedProfileId || mutating) return;
    setMutating(true);
    setMessage(null);
    try {
      await putClientBinding(selectedClient.id, selectedProfileId);
      setClients((current) => current.map((client) => client.id === selectedClient.id ? { ...client, profile_id: selectedProfileId } : client));
      setMessage("Профиль привязан");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Не удалось привязать профиль.");
    } finally {
      setMutating(false);
    }
  }

  async function unbindProfile(): Promise<void> {
    if (!selectedClient || !selectedClient.profile_id || mutating || selectedClient.token_kind === "legacy_ctl") return;
    setMutating(true);
    setMessage(null);
    try {
      await deleteClientBinding(selectedClient.id);
      setClients((current) => current.map((client) => client.id === selectedClient.id ? { ...client, profile_id: null } : client));
      setSelectedProfileId("");
      setMessage("Профиль отвязан");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Не удалось отвязать профиль.");
    } finally {
      setMutating(false);
    }
  }

  async function rotateToken(): Promise<void> {
    if (!selectedClient || !supportsManagedActions || mutating) return;
    setMutating(true);
    setMessage(null);
    try {
      setToken(await rotateMcpToken(selectedClient.id));
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Не удалось ротировать managed token.");
    } finally {
      setMutating(false);
    }
  }

  async function revokeToken(): Promise<void> {
    if (!selectedClient || !supportsManagedActions || mutating) return;
    setMutating(true);
    setMessage(null);
    try {
      await revokeMcpToken(selectedClient.id);
      setClients((current) => current.map((client) => client.id === selectedClient.id ? { ...client, status: "revoked" } : client));
      setMessage("Managed token отозван");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Не удалось отозвать managed token.");
    } finally {
      setMutating(false);
    }
  }

  return (
    <>
      <header className="topbar"><div><span className="eyebrow">CLIENT ACCESS / 03</span><h1>Клиенты</h1></div><button className="button secondary topbar-action" type="button" onClick={() => void load()}>Обновить</button></header>
      <div className="content-wrap">
        <section className="intro"><div><p className="section-kicker">MANAGED CONNECTIONS / INVENTORY</p><h2>Подключения MCP-клиентов</h2><p className="lede">Инвентарь показывает только метаданные. Bearer появляется только после явной выдачи и исчезает при уходе со страницы.</p></div><div className={`data-badge state-${loadState}`} role="status"><span className="state-dot" aria-hidden="true" />{loadState === "loading" ? "Загрузка клиентов" : stateLabel(loadState)}</div></section>
        {token && <div className="token-callout" role="alert"><strong>Bearer выдан один раз</strong><span>Скопируйте его сейчас. Он не хранится в интерфейсе после навигации или перезагрузки.</span><code>{token.access_token}</code><div className="button-row"><button className="button primary" type="button" onClick={() => void copyToken()}>Скопировать bearer</button><button className="text-button" type="button" onClick={() => setToken(null)}>Скрыть</button></div></div>}
        {message && <div className="state-panel state-error card standalone-state" role="alert"><span>{message}</span></div>}
        {loadState === "loading" && <div className="state-panel card standalone-state" role="status"><span className="loader" aria-hidden="true" />Загрузка клиентов</div>}
        {loadState === "error" && <div className="state-panel card standalone-state state-error" role="alert"><strong>Не удалось загрузить клиентов</strong><button className="button secondary" type="button" onClick={() => void load()}>Повторить</button></div>}
        {loadState === "empty" && <div className="empty-card card"><span className="empty-mark" aria-hidden="true">+</span><h3>Клиентов пока нет</h3><p>Выдайте первый managed token для подключения MCP-клиента.</p></div>}
        {loadState === "ready" && <div className="clients-grid"><section className="card client-inventory" aria-labelledby="client-inventory-title"><div className="card-heading"><div><p className="section-kicker">INVENTORY</p><h3 id="client-inventory-title">Зарегистрированные клиенты</h3></div><span className="chip">{clients.length} записей</span></div><div className="client-list">{clients.map((client) => <article className={`client-row ${selectedClientId === client.id ? "selected" : ""}`} key={client.id}><div><strong>{client.client_id}</strong><span>{clientKindLabel(client.token_kind)} · {clientStatusLabel(client.status)}</span></div><button className="text-button" type="button" onClick={() => selectClient(client)}>Выбрать {client.client_id}</button><dl><div><dt>Профиль</dt><dd>{profileName(client.profile_id)}</dd></div><div><dt>Scope</dt><dd>{client.scope ?? "Не задан"}</dd></div></dl></article>)}</div></section><section className="card client-controls" aria-labelledby="client-controls-title"><p className="section-kicker">SELECTED CLIENT</p><h3 id="client-controls-title">Доступ и токен</h3>{selectedClient ? <><p className="muted">{selectedClient.client_id} · {clientKindLabel(selectedClient.token_kind)}</p><label>Профиль для выбранного клиента<select value={selectedProfileId} onChange={(event) => setSelectedProfileId(event.target.value)}><option value="">Без профиля</option>{profiles.map((profile) => <option value={profile.id} key={profile.id}>{profile.name || profile.id}</option>)}</select></label><div className="button-row"><button className="button secondary" type="button" onClick={() => void bindProfile()} disabled={!selectedProfileId || mutating}>Привязать профиль</button><button className="button secondary" type="button" onClick={() => void unbindProfile()} disabled={!selectedClient.profile_id || mutating || selectedClient.token_kind === "legacy_ctl"}>Снять привязку</button></div><div className="button-row"><button className="button secondary" type="button" onClick={() => void rotateToken()} disabled={!supportsManagedActions || mutating}>Ротировать токен</button><button className="button danger" type="button" onClick={() => void revokeToken()} disabled={!supportsManagedActions || mutating}>Отозвать токен</button></div>{!supportsManagedActions && <p className="field-help">OAuth-клиенты управляются через Авторизация; legacy-клиенты нельзя ротировать или отзывать из этого интерфейса.</p>}</> : <p className="muted">Выберите клиента из инвентаря.</p>}</section><section className="card token-issue-card" aria-labelledby="issue-title"><p className="section-kicker">MANAGED JWT</p><h3 id="issue-title">Выдать managed token</h3><p className="muted">Токен будет показан только в одноразовом callout.</p><label>Client ID<input value={clientId} onChange={(event) => setClientId(event.target.value)} /></label><label>Срок действия, дней<input type="number" min="1" max="3650" value={ttlDays} onChange={(event) => setTtlDays(event.target.value)} /></label><label>Режим доступа<select value={accessMode} onChange={(event) => setAccessMode(event.target.value as AccessMode)}><option value="readonly">Только чтение</option><option value="full">Полный доступ</option></select></label><button className="button primary" type="button" onClick={() => void issueToken()} disabled={!clientId.trim() || issuing}>{issuing ? "Выдаём…" : "Выдать managed token"}</button></section></div>}
      </div>
    </>
  );
}

function AuthScreen() {
  const [rotating, setRotating] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  async function rotate(): Promise<void> {
    setRotating(true);
    setMessage(null);
    try {
      const result = await rotateOAuth();
      setMessage(result.message || (result.restart_required ? "OAuth secret обновлён. Перезапустите Hub." : "OAuth secret обновлён."));
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Не удалось обновить OAuth secret.");
    } finally {
      setRotating(false);
    }
  }

  return <><header className="topbar"><div><span className="eyebrow">AUTHENTICATION / 04</span><h1>Авторизация</h1></div></header><div className="content-wrap"><section className="intro"><div><p className="section-kicker">OAUTH CLIENT SECRET</p><h2>Управление доступом Hub</h2><p className="lede">Секрет OAuth никогда не показывается в UI. Ротация инвалидирует прежний секрет и может потребовать перезапуска Hub.</p></div></section><section className="card auth-card"><h3>OAuth secret</h3><p className="muted">Используйте ротацию только при плановом обновлении или подозрении на компрометацию.</p><button className="button primary" type="button" onClick={() => void rotate()} disabled={rotating}>{rotating ? "Обновляем…" : "Ротировать OAuth secret"}</button>{message && <p className="success-text" role="status">{message}</p>}</section></div></>;
}

export default function App() {
  const [view, setView] = useState<View>("instructions");

  useEffect(() => {
    const onHashChange = () => {
      const hash = window.location.hash;
      setView(hash === "#profiles" || hash === "#clients" || hash === "#auth" ? hash.slice(1) as View : "instructions");
    };
    onHashChange();
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  return (
    <div className="app-shell">
      <aside className="sidebar"><div className="brand"><span className="brand-mark" aria-hidden="true">G</span><span>GPTAdmin</span></div><div className="workspace-label">ОПЕРАЦИОННАЯ КОНСОЛЬ</div><nav aria-label="Основная навигация">{navigation.map((item) => <a className={`nav-item ${view === item.id ? "active" : ""}`} href={item.href} aria-current={view === item.id ? "page" : undefined} key={item.id} onClick={(event) => { event.preventDefault(); setView(item.id); window.history.replaceState(null, "", item.href); }}>{<><span className="nav-dot" aria-hidden="true" /><span>{item.label}</span></>}</a>)}<a className="nav-item" href="/admin/legacy/"><span className="nav-dot" aria-hidden="true" /><span>Операции и MCP</span></a></nav><div className="sidebar-footer"><span className="profile-state">{view === "profiles" ? "Профильный доступ" : "Рабочий контекст"}</span><a className="logout-link" href="/admin/logout">Выйти</a></div></aside>
      <main className="main-content">{view === "instructions" ? <InstructionsScreen /> : view === "profiles" ? <ProfilesScreen /> : view === "clients" ? <ClientsScreen /> : <AuthScreen />}</main>
    </div>
  );
}
