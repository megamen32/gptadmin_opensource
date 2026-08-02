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

type View = "instructions" | "profiles" | "clients" | "webhooks" | "auth";
type LoadState = "loading" | "ready" | "empty" | "error" | "stale";

const navigation: Array<{ id: View; label: string; href: string }> = [
  { id: "instructions", label: "Инструкции", href: "#instructions" },
  { id: "profiles", label: "Профили", href: "#profiles" },
  { id: "clients", label: "Клиенты", href: "#clients" },
  { id: "webhooks", label: "Вебхуки и агенты", href: "#webhooks" },
  { id: "auth", label: "Авторизация", href: "#auth" },
];

function viewFromHash(): View {
  const hash = window.location.hash;
  return hash === "#profiles" || hash === "#clients" || hash === "#webhooks" || hash === "#auth" ? hash.slice(1) as View : "instructions";
}

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

type WebhookRouteSummary = {
  id: string;
  kind: "mcp" | "prompt" | "shell";
  target: string;
  tool?: string;
  auth_mode: "hmac" | "token";
  callback_configured: boolean;
};

type WebhookRouteDraft = {
  id: string;
  authMode: "hmac" | "token";
  secret: string;
  signatureVersion: "v1" | "v2";
  maxSkewSeconds: string;
  kind: "mcp" | "prompt" | "shell";
  target: string;
  approvalMode: "" | "ask_before_write" | "bounded_autonomous";
  tool: string;
  argumentsJson: string;
  prompt: string;
  promptArg: string;
  command: string;
  cwd: string;
  callbackUrl: string;
  callbackAuthMode: "none" | "token" | "hmac";
  callbackSecret: string;
};

type WebhookJob = {
  job_id: string;
  route_id: string;
  status: string;
  created_at?: string;
  started_at?: string;
  completed_at?: string;
  error?: string;
  callback_status?: string;
  result?: Record<string, unknown>;
};

const emptyWebhookRoute = (): WebhookRouteDraft => ({
  id: "",
  authMode: "hmac",
  secret: "",
  signatureVersion: "v2",
  maxSkewSeconds: "300",
  kind: "mcp",
  target: "hub",
  approvalMode: "",
  tool: "",
  argumentsJson: "{}",
  prompt: "",
  promptArg: "",
  command: "",
  cwd: "",
  callbackUrl: "",
  callbackAuthMode: "none",
  callbackSecret: "",
});

async function webhookRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      ...(init.body ? { "Content-Type": "application/json" } : {}),
      ...init.headers,
    },
  });
  if (!response.ok) {
    let detail = `HTTP ${response.status}`;
    try {
      const body = await response.json() as { detail?: unknown };
      if (typeof body.detail === "string") detail = body.detail;
    } catch {
      // Keep the bounded status-only fallback; never persist an arbitrary body.
    }
    throw new ApiError(response.status, detail);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

function asRouteSummary(value: unknown): WebhookRouteSummary | null {
  if (typeof value !== "object" || value === null) return null;
  const route = value as Record<string, unknown>;
  if (typeof route.id !== "string" || typeof route.kind !== "string" || typeof route.target !== "string") return null;
  if (route.kind !== "mcp" && route.kind !== "prompt" && route.kind !== "shell") return null;
  return {
    id: route.id,
    kind: route.kind,
    target: route.target,
    tool: typeof route.tool === "string" ? route.tool : undefined,
    auth_mode: route.auth_mode === "token" ? "token" : "hmac",
    callback_configured: route.callback_configured === true,
  };
}

function scrubJobValue(value: unknown, depth = 0): unknown {
  if (depth >= 8) return "[скрыто: превышена глубина]";
  if (Array.isArray(value)) return value.map((item) => scrubJobValue(item, depth + 1));
  if (typeof value === "object" && value !== null) {
    return Object.fromEntries(Object.entries(value as Record<string, unknown>)
      .filter(([key]) => !/(secret|token|password|authorization|credential)/i.test(key))
      .map(([key, nested]) => [key, scrubJobValue(nested, depth + 1)]));
  }
  return value;
}

function visibleJobResult(result: Record<string, unknown>): Array<[string, string]> {
  return Object.entries(result)
    .filter(([key]) => !/(secret|token|password|authorization|credential)/i.test(key))
    .map(([key, value]) => [
      key,
      typeof value === "object" && value !== null
        ? JSON.stringify(scrubJobValue(value))
        : String(value),
    ]);
}

function WebhooksScreen() {
  const [loadState, setLoadState] = useState<LoadState>("loading");
  const [routes, setRoutes] = useState<WebhookRouteSummary[]>([]);
  const [draft, setDraft] = useState<WebhookRouteDraft>(emptyWebhookRoute);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [mutating, setMutating] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [jobId, setJobId] = useState("");
  const [job, setJob] = useState<WebhookJob | null>(null);
  const [jobMessage, setJobMessage] = useState<string | null>(null);
  const [checkingJob, setCheckingJob] = useState(false);

  async function loadRoutes(): Promise<void> {
    setLoadState("loading");
    setMessage(null);
    try {
      const response = await webhookRequest<{ routes?: unknown[] }>("/webhook-routes");
      const summaries = (response.routes ?? []).map(asRouteSummary).filter((route): route is WebhookRouteSummary => route !== null);
      setRoutes(summaries);
      setLoadState(summaries.length ? "ready" : "empty");
    } catch (error) {
      setLoadState("error");
      setMessage(error instanceof Error ? error.message : "Не удалось загрузить маршруты.");
    }
  }

  useEffect(() => { void loadRoutes(); }, []);

  function startCreate(): void {
    setEditingId(null);
    setDraft(emptyWebhookRoute());
    setConfirmDelete(false);
    setMessage(null);
  }

  function startEdit(route: WebhookRouteSummary): void {
    setEditingId(route.id);
    setDraft({
      ...emptyWebhookRoute(),
      id: route.id,
      authMode: route.auth_mode,
      kind: route.kind,
      target: route.target,
      tool: route.tool ?? "",
      callbackAuthMode: route.callback_configured ? "hmac" : "none",
    });
    setConfirmDelete(false);
    setMessage("Для полной замены повторно введите секрет и поля действия. Сохранённые секреты Hub не возвращает.");
  }

  function buildRoute(): Record<string, unknown> {
    const id = draft.id.trim();
    const secret = draft.secret.trim();
    if (!id || id.includes("/") || id.includes("\\")) throw new Error("Укажите ID одним сегментом пути.");
    if (!secret) throw new Error("Введите секрет маршрута. Hub хранит его только на запись.");
    if (!draft.target.trim()) throw new Error("Укажите цель действия.");

    let argumentsValue: Record<string, unknown> | undefined;
    if (draft.argumentsJson.trim()) {
      const parsed = JSON.parse(draft.argumentsJson) as unknown;
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) throw new Error("Аргументы должны быть JSON-объектом.");
      argumentsValue = parsed as Record<string, unknown>;
    }

    const action: Record<string, unknown> = { kind: draft.kind, target: draft.target.trim() };
    if (draft.approvalMode) action.approval_mode = draft.approvalMode;
    if (draft.kind === "shell") {
      if (!draft.command.trim()) throw new Error("Для Shell укажите фиксированную команду.");
      action.command = draft.command;
      if (draft.cwd.trim()) action.cwd = draft.cwd.trim();
    } else {
      if (!draft.tool.trim()) throw new Error("Для MCP или prompt укажите инструмент.");
      action.tool = draft.tool.trim();
      if (argumentsValue && Object.keys(argumentsValue).length) action.arguments = argumentsValue;
      if (draft.kind === "prompt") {
        if (!draft.prompt.trim()) throw new Error("Для prompt укажите шаблон сообщения.");
        action.prompt = draft.prompt;
        if (draft.promptArg.trim()) action.prompt_arg = draft.promptArg.trim();
      }
    }

    const route: Record<string, unknown> = { id, action };
    if (draft.authMode === "hmac") {
      route.hmac_secret = secret;
      route.signature_version = draft.signatureVersion;
      const skew = Number(draft.maxSkewSeconds);
      if (Number.isFinite(skew) && skew > 0) route.max_skew_seconds = Math.floor(skew);
    } else {
      route.token = secret;
    }
    if (draft.callbackUrl.trim()) {
      const callback: Record<string, unknown> = { url: draft.callbackUrl.trim() };
      if (draft.callbackAuthMode !== "none") {
        if (!draft.callbackSecret.trim()) throw new Error("Введите секрет callback или выберите режим без авторизации.");
        callback[draft.callbackAuthMode === "token" ? "token" : "hmac_secret"] = draft.callbackSecret.trim();
      }
      route.callback = callback;
    }
    return route;
  }

  async function saveRoute(): Promise<void> {
    if (mutating) return;
    setMutating(true);
    setMessage(null);
    try {
      const route = buildRoute();
      const replacing = editingId !== null;
      const path = replacing ? `/webhook-routes/${encodeURIComponent(editingId)}` : "/webhook-routes";
      const summary = asRouteSummary(await webhookRequest<unknown>(path, {
        method: replacing ? "PUT" : "POST",
        body: JSON.stringify(route),
      }));
      if (!summary) throw new Error("Hub вернул некорректное описание маршрута.");
      setRoutes((current) => replacing ? current.map((item) => item.id === editingId ? summary : item) : [...current, summary]);
      setLoadState("ready");
      setEditingId(summary.id);
      setDraft((current) => ({ ...current, id: summary.id, secret: "", callbackSecret: "" }));
      setMessage(replacing ? "Маршрут заменён" : "Маршрут создан");
    } catch (error) {
      setMessage(error instanceof SyntaxError ? "Аргументы должны быть корректным JSON-объектом." : error instanceof Error ? error.message : "Не удалось сохранить маршрут.");
    } finally {
      setMutating(false);
    }
  }

  async function deleteRoute(): Promise<void> {
    if (!editingId || mutating || !confirmDelete) return;
    const deletedId = editingId;
    setMutating(true);
    setMessage(null);
    try {
      await webhookRequest<void>(`/webhook-routes/${encodeURIComponent(deletedId)}`, { method: "DELETE" });
      const remaining = routes.filter((route) => route.id !== deletedId);
      setRoutes(remaining);
      setLoadState(remaining.length ? "ready" : "empty");
      setDraft(emptyWebhookRoute());
      setEditingId(null);
      setConfirmDelete(false);
      setMessage("Маршрут удалён");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Не удалось удалить маршрут.");
    } finally {
      setMutating(false);
    }
  }

  async function inspectJob(): Promise<void> {
    const id = jobId.trim();
    if (!id || checkingJob) return;
    setCheckingJob(true);
    setJob(null);
    setJobMessage(null);
    try {
      setJob(await webhookRequest<WebhookJob>(`/admin/api/webhook-jobs/${encodeURIComponent(id)}`));
    } catch (error) {
      setJobMessage(error instanceof Error ? error.message : "Не удалось получить задание.");
    } finally {
      setCheckingJob(false);
    }
  }

  const editingRoute = editingId ? routes.find((route) => route.id === editingId) : null;
  const resultEntries = job?.result ? visibleJobResult(job.result) : [];

  return (
    <>
      <header className="topbar"><div><span className="eyebrow">WEBHOOK AUTOMATION / 04</span><h1>Вебхуки и агенты</h1></div><button className="button primary topbar-action" type="button" onClick={startCreate}>Новый маршрут</button></header>
      <div className="content-wrap">
        <section className="intro"><div><p className="section-kicker">SIGNED ROUTES / AGENT JOBS</p><h2>Управление маршрутами Hub</h2><p className="lede">Маршрут фиксирует авторизацию и разрешённое действие. Секреты принимаются только при записи и никогда не показываются из ответа Hub.</p></div><div className={`data-badge state-${loadState}`} role="status"><span className="state-dot" aria-hidden="true" />{loadState === "loading" ? "Загрузка маршрутов" : stateLabel(loadState)}</div></section>

        {loadState === "loading" && <div className="state-panel card standalone-state" role="status"><span className="loader" aria-hidden="true" />Загрузка маршрутов</div>}
        {loadState === "error" && <div className="state-panel card standalone-state state-error" role="alert"><strong>Не удалось загрузить маршруты</strong><span>{message}</span><button className="button secondary" type="button" onClick={() => void loadRoutes()}>Повторить</button></div>}
        {loadState !== "loading" && loadState !== "error" && <section className="webhook-layout">
          <aside className="route-list card" aria-label="Список webhook-маршрутов">
            <div className="list-heading"><div><p className="section-kicker">МАРШРУТЫ</p><h3>Разрешённые действия</h3></div><button className="text-button" type="button" onClick={startCreate}>+ Новый</button></div>
            {routes.length === 0 && <div className="route-empty"><strong>Маршрутов пока нет</strong><span>Создайте первый подписанный маршрут.</span></div>}
            {routes.map((route) => <article className={`route-row ${editingId === route.id ? "selected" : ""}`} key={route.id}><div><strong>{route.id}</strong><span>{route.target}</span><small>{route.kind.toUpperCase()} · {route.auth_mode === "hmac" ? "HMAC" : "Bearer"}{route.callback_configured ? " · callback" : ""}</small></div><button className="text-button" type="button" onClick={() => startEdit(route)}>Изменить {route.id}</button></article>)}
          </aside>

          <section className="card route-editor" aria-labelledby="route-editor-title">
            <div className="card-heading"><div><p className="section-kicker">{editingId ? "REPLACE ROUTE" : "CREATE ROUTE"}</p><h3 id="route-editor-title">{editingId ? `Заменить ${editingId}` : "Новый маршрут"}</h3></div><span className="chip">Секрет: только запись</span></div>
            {message && <div className={message.includes("создан") || message.includes("заменён") || message.includes("удалён") ? "form-message success-text" : "form-message warning-text"} role="status">{message}</div>}
            <form className="route-form" onSubmit={(event) => { event.preventDefault(); void saveRoute(); }}>
              <div className="form-grid">
                <label>Идентификатор маршрута<input value={draft.id} disabled={editingId !== null} onChange={(event) => setDraft({ ...draft, id: event.target.value })} /></label>
                <label>Режим авторизации<select value={draft.authMode} onChange={(event) => setDraft({ ...draft, authMode: event.target.value as WebhookRouteDraft["authMode"] })}><option value="hmac">HMAC</option><option value="token">Bearer token</option></select></label>
                <label>Секрет маршрута<input aria-label="Секрет маршрута" type="password" autoComplete="new-password" value={draft.secret} onChange={(event) => setDraft({ ...draft, secret: event.target.value })} /><small>Не загружается из Hub и очищается после записи.</small></label>
                {draft.authMode === "hmac" && <label>Версия подписи<select value={draft.signatureVersion} onChange={(event) => setDraft({ ...draft, signatureVersion: event.target.value as WebhookRouteDraft["signatureVersion"] })}><option value="v2">v2</option><option value="v1">v1</option></select></label>}
                {draft.authMode === "hmac" && <label>Допустимое отклонение, секунд<input type="number" min="1" value={draft.maxSkewSeconds} onChange={(event) => setDraft({ ...draft, maxSkewSeconds: event.target.value })} /></label>}
                <label>Тип действия<select value={draft.kind} onChange={(event) => { const kind = event.target.value as WebhookRouteDraft["kind"]; setDraft({ ...draft, kind, target: kind !== draft.kind ? "" : draft.target }); }}><option value="mcp">MCP</option><option value="prompt">Prompt</option><option value="shell">Shell</option></select></label>
                <label>Цель<input value={draft.target} onChange={(event) => setDraft({ ...draft, target: event.target.value })} placeholder="hub или shell:machine" /></label>
                <label>Режим подтверждения<select value={draft.approvalMode} onChange={(event) => setDraft({ ...draft, approvalMode: event.target.value as WebhookRouteDraft["approvalMode"] })}><option value="">По умолчанию</option><option value="ask_before_write">Запрос перед записью</option><option value="bounded_autonomous">Ограниченно автономный</option></select></label>
              </div>
              {draft.kind === "shell" ? <div className="form-grid"><label>Команда<input value={draft.command} onChange={(event) => setDraft({ ...draft, command: event.target.value })} /></label><label>Рабочий каталог<input value={draft.cwd} onChange={(event) => setDraft({ ...draft, cwd: event.target.value })} /></label></div> : <><div className="form-grid"><label>Инструмент<input value={draft.tool} onChange={(event) => setDraft({ ...draft, tool: event.target.value })} /></label>{draft.kind === "prompt" && <label>Аргумент prompt<input value={draft.promptArg} onChange={(event) => setDraft({ ...draft, promptArg: event.target.value })} placeholder="message" /></label>}</div><label>Аргументы JSON<textarea className="short-textarea" value={draft.argumentsJson} onChange={(event) => setDraft({ ...draft, argumentsJson: event.target.value })} spellCheck="false" /></label>{draft.kind === "prompt" && <label>Шаблон сообщения<textarea className="short-textarea" value={draft.prompt} onChange={(event) => setDraft({ ...draft, prompt: event.target.value })} /></label>}</>}
              <fieldset className="callback-fieldset"><legend>Callback (необязательно)</legend><div className="form-grid"><label>URL callback<input type="url" value={draft.callbackUrl} onChange={(event) => setDraft({ ...draft, callbackUrl: event.target.value })} /></label><label>Авторизация callback<select value={draft.callbackAuthMode} onChange={(event) => setDraft({ ...draft, callbackAuthMode: event.target.value as WebhookRouteDraft["callbackAuthMode"] })}><option value="none">Без авторизации</option><option value="hmac">HMAC</option><option value="token">Bearer token</option></select></label>{draft.callbackAuthMode !== "none" && <label>Секрет callback<input type="password" autoComplete="new-password" value={draft.callbackSecret} onChange={(event) => setDraft({ ...draft, callbackSecret: event.target.value })} /></label>}</div>{editingRoute?.callback_configured && <p className="field-help">Текущий callback скрыт. Чтобы сохранить его при замене, повторно заполните URL и авторизацию.</p>}</fieldset>
              <div className="route-actions"><button className="button primary" type="submit" disabled={mutating}>{mutating ? "Сохраняем…" : editingId ? "Заменить маршрут" : "Создать маршрут"}</button>{editingId && <button className="button danger" type="button" onClick={() => setConfirmDelete(true)} disabled={mutating}>Удалить маршрут</button>}</div>
            </form>
            {confirmDelete && editingId && <div className="delete-confirmation" role="alertdialog" aria-modal="true" aria-labelledby="delete-route-title" onKeyDown={(event) => { if (event.key === "Escape") setConfirmDelete(false); }}><strong id="delete-route-title">Удалить маршрут {editingId}?</strong><p>Маршрут перестанет принимать новые события. Это действие требует явного подтверждения.</p><div className="button-row"><button className="button secondary" type="button" onClick={() => setConfirmDelete(false)}>Отмена</button><button className="button danger" type="button" autoFocus onClick={() => void deleteRoute()} disabled={mutating}>Подтвердить удаление</button></div></div>}
          </section>

          <section className="card job-inspector" aria-labelledby="job-inspector-title">
            <div><p className="section-kicker">DURABLE JOB STATUS</p><h3 id="job-inspector-title">Проверить webhook-задание</h3><p className="muted">Укажите один ID задания. Интерфейс показывает статус и безопасные поля результата.</p></div>
            <form className="job-search" onSubmit={(event) => { event.preventDefault(); void inspectJob(); }}><label>ID задания<input value={jobId} onChange={(event) => setJobId(event.target.value)} /></label><button className="button secondary" type="submit" disabled={!jobId.trim() || checkingJob}>{checkingJob ? "Проверяем…" : "Проверить задание"}</button></form>
            {jobMessage && <div className="state-panel state-error compact" role="alert"><strong>Не удалось получить задание</strong><span>{jobMessage}</span><button className="button secondary" type="button" onClick={() => void inspectJob()}>Повторить</button></div>}
            {job && <div className="job-result" role="status"><dl><div><dt>ID</dt><dd>{job.job_id}</dd></div><div><dt>Маршрут</dt><dd>{job.route_id}</dd></div><div><dt>Статус</dt><dd>{job.status}</dd></div>{job.created_at && <div><dt>Создано</dt><dd>{formatUpdated(job.created_at)}</dd></div>}{job.started_at && <div><dt>Запущено</dt><dd>{formatUpdated(job.started_at)}</dd></div>}{job.completed_at && <div><dt>Завершено</dt><dd>{formatUpdated(job.completed_at)}</dd></div>}{job.callback_status && <div><dt>Callback</dt><dd>{job.callback_status}</dd></div>}{job.error && <div><dt>Ошибка</dt><dd>{job.error}</dd></div>}</dl>{resultEntries.length > 0 && <div className="safe-result"><strong>Результат</strong><dl>{resultEntries.map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl></div>}</div>}
          </section>
        </section>}
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
  const [view, setView] = useState<View>(viewFromHash);

  useEffect(() => {
    const onHashChange = () => {
      setView(viewFromHash());
    };
    onHashChange();
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  return (
    <div className="app-shell">
      <aside className="sidebar"><div className="brand"><span className="brand-mark" aria-hidden="true">G</span><span>GPTAdmin</span></div><div className="workspace-label">ОПЕРАЦИОННАЯ КОНСОЛЬ</div><nav aria-label="Основная навигация">{navigation.map((item) => <a className={`nav-item ${view === item.id ? "active" : ""}`} href={item.href} aria-current={view === item.id ? "page" : undefined} key={item.id} onClick={(event) => { event.preventDefault(); setView(item.id); window.history.replaceState(null, "", item.href); }}>{<><span className="nav-dot" aria-hidden="true" /><span>{item.label}</span></>}</a>)}<a className="nav-item" href="/admin/legacy/"><span className="nav-dot" aria-hidden="true" /><span>Операции и MCP</span></a></nav><div className="sidebar-footer"><span className="profile-state">{view === "profiles" ? "Профильный доступ" : "Рабочий контекст"}</span><a className="logout-link" href="/admin/logout">Выйти</a></div></aside>
      <main className="main-content">{view === "instructions" ? <InstructionsScreen /> : view === "profiles" ? <ProfilesScreen /> : view === "clients" ? <ClientsScreen /> : view === "webhooks" ? <WebhooksScreen /> : <AuthScreen />}</main>
    </div>
  );
}
