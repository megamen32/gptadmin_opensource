import { logicalConnections, shortId } from "./connections";
import JobsScreen from "./JobsScreen";
import AgentsScreen from "./AgentsScreen";
import ResourcesScreen from "./ResourcesScreen";
import ToolsScreen from "./ToolsScreen";
import RawScreen from "./RawScreen";
import AuditScreen from "./AuditScreen";
import OverviewScreen from "./OverviewScreen";
import AccessOperations from "./AccessOperations";
import McpManageScreen from "./McpManageScreen";
import FailoverScreen from "./FailoverScreen";
import SecurityScreen from "./SecurityScreen";
import { useEffect, useRef, useState } from "react";
import {
  ApiError,
  byteLength,
  getAccessProfiles,
  getClients,
  getStoredMcpToken,
  setClientRole,
  getAccessProfile,
  getDefaultInstructionSet,
	getVirtualMCPs,
  getHubSettings,
  getSettingsHistory,
  rollbackHubSettings,
  getRegistryMaintenance,
  runRegistryCleanup,
  setAgentCleanupPolicy,
  getAgentTombstones,
  getHubDiagnose,
  startHubHandover,
  INSTRUCTION_LIMIT,
  issueMcpToken,
  putClientBinding,
  putAccessProfile,
  putDefaultInstructionSet,
  revokeMcpToken,
  rotateOAuth,
	setVirtualMCP,
  setHubSettings,
  rotateMcpToken,
  deleteClientBinding,
  type ClientInventoryItem,
  type TokenResponse,
  type AccessMode,
  type AccessProfile,
  type ExternalWorkspaceRef,
  type InstructionSet,
	type VirtualMCP,
  type HubSettingDefinition,
  type SettingsRevision,
  type CleanupCandidate,
  type AgentTombstone,
} from "./api";
import "./styles.css";

type View = "overview" | "audit" | "raw" | "tools" | "resources" | "agents" | "jobs" | "mcpmanage" | "failover" | "security" | "operations" | "instructions" | "profiles" | "clients" | "webhooks" | "auth" | "capabilities";
type LoadState = "loading" | "ready" | "empty" | "error" | "stale";

type NavigationSection = {
  id: string;
  label: string;
  target: View;
  views: readonly View[];
};

type ContextLink = { id: View; label: string };

const primaryNavigation: NavigationSection[] = [
  { id: "overview", label: "Обзор", target: "overview", views: ["overview"] },
  { id: "infrastructure", label: "Серверы", target: "agents", views: ["agents", "mcpmanage", "resources", "failover"] },
  { id: "access", label: "Доступ", target: "clients", views: ["clients", "profiles", "auth"] },
  { id: "automation", label: "Автоматизация", target: "webhooks", views: ["webhooks"] },
  { id: "jobs", label: "Задачи", target: "jobs", views: ["jobs", "tools"] },
  { id: "journal", label: "Журнал", target: "audit", views: ["audit", "operations", "raw"] },
];

const contextualNavigation: Partial<Record<string, ContextLink[]>> = {
  infrastructure: [
    { id: "agents", label: "Состояние" },
    { id: "mcpmanage", label: "Сервисы MCP" },
    { id: "resources", label: "Ресурсы MCP" },
    { id: "failover", label: "Резервирование" },
  ],
  access: [
    { id: "clients", label: "Клиенты" },
    { id: "profiles", label: "Профили" },
    { id: "auth", label: "Подключение" },
  ],
  jobs: [
    { id: "jobs", label: "Очередь" },
    { id: "tools", label: "Ручной запуск" },
  ],
  journal: [
    { id: "audit", label: "События" },
    { id: "operations", label: "Изменения доступа" },
    { id: "raw", label: "Технические данные" },
  ],
  settings: [
    { id: "instructions", label: "Инструкции" },
    { id: "capabilities", label: "Системные настройки" },
    { id: "security", label: "Безопасность" },
  ],
};

const settingsSection: NavigationSection = {
  id: "settings",
  label: "Настройки",
  target: "instructions",
  views: ["capabilities", "security", "instructions"],
};


const advancedViews = new Set<View>(["mcpmanage", "resources", "failover", "tools", "raw", "capabilities", "security"]);

function isAdvancedView(view: View): boolean {
  return advancedViews.has(view);
}

const allViews: readonly View[] = [
  "overview", "agents", "jobs", "operations", "mcpmanage", "tools", "resources",
  "instructions", "profiles", "clients", "webhooks", "auth", "capabilities",
  "failover", "security", "audit", "raw",
];

function viewFromHash(): View {
  const view = window.location.hash.slice(1).split("?")[0] as View;
  return allViews.includes(view) ? view : "overview";
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
  const [gptPrompt, setGptPrompt] = useState<string | null>(null);
  const [gptPromptError, setGptPromptError] = useState(false);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const fillPlaceholders = (text: string): string => {
      const origin = window.location.origin;
      return text.split("{{OPENAPI_URL}}").join(`${origin}/actions/openapi.yaml`).split("{{MCP_URL}}").join(`${origin}/mcp`);
    };
    fetch("/actions/instructions.md")
      .then((response) => (response.ok ? response.text() : Promise.reject(new Error(String(response.status)))))
      .then((text) => { if (!cancelled) setGptPrompt(fillPlaceholders(text)); })
      .catch(() => { if (!cancelled) setGptPromptError(true); });
    return () => { cancelled = true; };
  }, []);

  async function copyGptPrompt(): Promise<void> {
    if (!gptPrompt) return;
    try {
      await navigator.clipboard.writeText(gptPrompt);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopied(false);
    }
  }

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
        <section className="profile-grid" aria-label="Инструкции для Custom GPT">
          <div className="editor-card card">
            <div className="card-heading"><div><h3>Custom GPT</h3><p className="muted">Готовый текст для редактора Custom GPT в ChatGPT: скопируйте и вставьте в Instructions</p></div><span className="chip">Actions only</span></div>
            {gptPrompt === null ? (
              <div className="state-panel" role="status"><strong>{gptPromptError ? "Не удалось загрузить текст инструкций" : "Загрузка текста…"}</strong>{gptPromptError && <span>Hub не вернул /actions/instructions.md.</span>}</div>
            ) : (
              <>
                <label className="editor-label" htmlFor="custom-gpt-prompt">Текст инструкций Custom GPT</label>
                <textarea id="custom-gpt-prompt" readOnly value={gptPrompt} spellCheck={false} />
                <div className="editor-footer"><span className="editor-hint">Endpoint-контракт: только discover → schema → execute → job из /actions/openapi.yaml</span></div>
                <div className="action-row"><a className="button secondary" href="/actions/openapi.yaml">Открыть OpenAPI</a><button className="button primary" type="button" onClick={() => void copyGptPrompt()}>{copied ? "Скопировано" : "Скопировать"}</button></div>
              </>
            )}
          </div>
          <aside className="details-column" aria-label="Сведения о Custom GPT"><div className="note-card"><span className="note-icon" aria-hidden="true">i</span><p><strong>Граница Custom GPT</strong><br />Этот GPT работает только через Actions-фасад. Нативный /mcp и shell-скрипты с вызовом /mcp запрещены инструкцией.</p></div></aside>
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
          <label>Режим доступа<select aria-label="Режим доступа" value={draft.access_mode} onChange={(event) => onChange({ ...draft, access_mode: event.target.value as AccessMode })}><option value="readonly">Только чтение</option><option value="full">Полный доступ</option></select></label>
          <label>Набор инструкций<input value={draft.instruction_set_id ?? "default"} onChange={(event) => onChange({ ...draft, instruction_set_id: event.target.value })} /><small>Идентификатор существующего набора инструкций</small></label><label>Режим выполнения<select aria-label="Режим выполнения" value={draft.access_mode === "readonly" ? "read_only" : draft.approval_mode ?? "bounded_autonomous"} disabled={draft.access_mode === "readonly"} onChange={(event) => onChange({ ...draft, approval_mode: event.target.value })}><option value="unrestricted">Без дополнительных ограничений</option><option value="read_only">Только чтение</option><option value="ask_before_write">Подтверждать изменения</option><option value="bounded_autonomous">Ограниченная автономность</option></select></label>
        </div>
        <p className="field-help">Звёздочка (*) разрешает все цели или инструменты. Пустой список не разрешает ничего.</p><label>Разрешённые цели<textarea className="short-textarea" value={listValue(draft.allowed_targets)} onChange={(event) => onChange({ ...draft, allowed_targets: parseList(event.target.value) })} placeholder="hub, shell:machine-a" /></label>
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
    const selectionAtStart = requestNumber.current;
    setListState("loading");
    try {
      const result = await getAccessProfiles();
      setProfiles(result);
      setListState(result.length ? "ready" : "empty");
      if (selectionAtStart !== requestNumber.current) return;
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
  useEffect(() => { if (selectedId && !creating && saved?.id !== selectedId) void loadProfile(selectedId); }, [selectedId, creating]);

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
  if (kind === "durable") return "Токен подключения";
  if (kind === "managed_jwt") return "Старый JWT";
  if (kind === "oauth_refresh") return "Обновление OAuth";
  if (kind === "configured_opaque_migration") return "Ключ из конфигурации";
  if (kind === "legacy_ctl") return "Legacy CTL";
  if (kind === "oauth") return "OAuth";
  return kind;
}

function clientStatusLabel(status: string): string {
  if (status === "active") return "Активен";
  if (status === "revoked") return "Отозван";
  if (status === "expired") return "Истёк";
  if (status === "registered") return "Зарегистрирован";
  if (status === "unregistered") return "Только история, регистрации нет";
  return status;
}

function ClientsScreen({ token, setToken }: { token: TokenResponse | null; setToken: (value: TokenResponse | null) => void }) {
  const [loadState, setLoadState] = useState<LoadState>("loading");
  const [filter, setFilter] = useState("");
  const [showInactive, setShowInactive] = useState(false);
  const linkedConnection = new URLSearchParams(window.location.hash.split("?")[1] ?? "").get("connection");
  const [clients, setClients] = useState<ClientInventoryItem[]>([]);
  const [profiles, setProfiles] = useState<AccessProfile[]>([]);
  const [issueRole, setIssueRole] = useState("client");
  const [selectedRole, setSelectedRole] = useState("client");
  const [clientId, setClientId] = useState("managed-client");
  const [issueProfileId, setIssueProfileId] = useState("");
  const [ttlDays, setTtlDays] = useState("7");
  const [accessMode, setAccessMode] = useState<AccessMode>("readonly");
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
      const connections = logicalConnections(clientResult);
      setSelectedClientId((current) => {
        const id = current || linkedConnection;
        return id && connections.some((client) => client.id === id) ? id : connections[0]?.id ?? null;
      });
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
      const result = await issueMcpToken({ client_id: clientId.trim(), ttl_days: Math.max(1, Number(ttlDays) || 7), access_mode: accessMode, profile_id: issueProfileId || undefined, role: issueRole });
      setToken(result);
      setClients((items) => [...items, { id: result.token_id, client_id: result.client_id || clientId, role: issueRole, token_kind: "durable", status: "active", access_mode: accessMode, profile_id: issueProfileId || null, scope: null, redirect_uris: [], issued_at: null, created_at: null, expires_at: null, revoked_at: null }]);
      setSelectedClientId(result.token_id);
      setSelectedRole(issueRole);
      setSelectedProfileId(issueProfileId);
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
  const connections = logicalConnections(clients);
  const visibleClients = connections.filter((client) => (showInactive || client.id === selectedClientId || !["revoked", "expired"].includes(client.status)) && `${client.label} ${client.client_id} ${client.id}`.toLowerCase().includes(filter.toLowerCase()));
  const selectedClient = connections.find((client) => client.id === selectedClientId) ?? null;
  const canManage = Boolean(selectedClient?.registered && selectedClient.token_kind !== "legacy_ctl" && !["revoked", "expired"].includes(selectedClient.status));
  const canReveal = Boolean(selectedClient && !["oauth", "legacy_ctl"].includes(selectedClient.token_kind));
  useEffect(() => {
    const selected = logicalConnections(clients).find((client) => client.id === selectedClientId);
    setSelectedRole(selected?.role ?? "client");
    setSelectedProfileId(selected?.profile_id ?? "");
  }, [clients, selectedClientId]);
  const supportsManagedActions = canManage && ["managed_jwt", "durable"].includes(selectedClient?.token_kind ?? "");

  function selectClient(client: ClientInventoryItem): void {
    setSelectedClientId(client.id);
    setSelectedRole(client.role || "client");
    setSelectedProfileId(client.profile_id ?? "");
    setMessage(null);
  }

  async function revealToken(): Promise<void> {
    if (!selectedClient || !canManage || mutating) return;
    setMutating(true); setMessage(null);
    try { setToken(await getStoredMcpToken(selectedClient.id)); }
    catch (error) { setMessage(error instanceof Error ? error.message : "Значение токена не было сохранено. Нужна явная ротация."); }
    finally { setMutating(false); }
  }
  async function saveRole(): Promise<void> {
    if (!selectedClient || mutating) return;
    setMutating(true); setMessage(null);
    try {
      await setClientRole(selectedClient.id, selectedRole);
      setClients((items) => items.map((item) => item.id === selectedClient.id ? { ...item, role: selectedRole } : item));
      setMessage("Роль сохранена на обоих Hub");
    } catch (error) { setMessage(error instanceof Error ? error.message : "Не удалось сохранить роль."); }
    finally { setMutating(false); }
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
        <section className="intro"><div><p className="section-kicker">MANAGED CONNECTIONS / INVENTORY</p><h2>Подключения MCP-клиентов</h2><p className="lede">Одна карточка — одно подключение. Роль и профиль OAuth назначаются подключению целиком; история обновления токенов находится внутри карточки.</p></div><div className={`data-badge state-${loadState}`} role="status"><span className="state-dot" aria-hidden="true" />{loadState === "loading" ? "Загрузка клиентов" : stateLabel(loadState)}</div></section>
        {token && <div className="token-callout" role="alert"><strong>Токен подключения</strong><span>Доступен также в разделе «Токены и подключение». Сохранён на Hub; доступен владельцу и администраторам после перезапуска.</span><code>{token.access_token}</code><div className="button-row"><button className="button primary" type="button" onClick={() => void copyToken()}>Скопировать bearer</button><button className="text-button" type="button" onClick={() => setToken(null)}>Скрыть</button></div></div>}
        {message && <div className="state-panel state-error card standalone-state" role="alert"><span>{message}</span></div>}
        {loadState === "loading" && <div className="state-panel card standalone-state" role="status"><span className="loader" aria-hidden="true" />Загрузка клиентов</div>}
        {loadState === "error" && <div className="state-panel card standalone-state state-error" role="alert"><strong>Не удалось загрузить клиентов</strong><button className="button secondary" type="button" onClick={() => void load()}>Повторить</button></div>}
        {loadState === "empty" && <div className="empty-card card"><span className="empty-mark" aria-hidden="true">+</span><h3>Клиентов пока нет</h3><p>Выдайте первый managed token для подключения MCP-клиента.</p></div>}
        {(loadState === "ready" || loadState === "empty") && <div className="clients-grid"><section className="card client-inventory" aria-labelledby="client-inventory-title"><div className="card-heading"><div><p className="section-kicker">INVENTORY</p><h3 id="client-inventory-title">Зарегистрированные клиенты</h3></div><span className="chip">{connections.length} подключений</span></div><div className="connection-filters"><label>Найти подключение<input type="search" value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="Название или точный ID" /></label><label className="inline-check"><input type="checkbox" checked={showInactive} onChange={(event) => setShowInactive(event.target.checked)} />Показывать отозванные и истёкшие</label></div>
          {linkedConnection && <p className="connection-notice">Подключение выбрано по точному ID из ссылки. Роль назначается ему, а не отдельным записям истории.</p>}
          <div className="client-list">{visibleClients.map((client) => <article className={`client-row ${selectedClientId === client.id ? "selected" : ""}`} key={client.id} data-connection-id={client.id}>
            <div className="client-identity"><strong>{client.label}</strong><span>{clientKindLabel(client.token_kind)} · {clientStatusLabel(client.status)}</span><small className="connection-id" title={client.id}>ID: {shortId(client.id)}</small></div>
            <button className="text-button" type="button" onClick={() => selectClient(client)} aria-label={`Выбрать ${client.client_id}`}>{selectedClientId === client.id ? "Выбрано" : "Выбрать"}</button>
            <dl><div><dt>Роль</dt><dd>{client.role === "owner" ? "Владелец" : client.role === "admin" ? "Администратор" : "Клиент"}</dd></div><div><dt>Профиль</dt><dd>{profileName(client.profile_id)}</dd></div></dl>
            {client.credentials.length > 0 && <details className="connection-history"><summary>История OAuth: {client.credentials.length} записей</summary><p>Это обновления токена одного подключения, не отдельные пользователи. История сохранена, права здесь не назначаются.</p>{client.credentials.map((credential) => <div className="credential-history-row" key={credential.id}><code title={credential.id}>{shortId(credential.id)}</code><span>{clientStatusLabel(credential.status)}</span><time>{credential.issued_at ? new Date(credential.issued_at * 1000).toLocaleString("ru-RU") : "Дата не сохранена"}</time></div>)}</details>}
          </article>)}{!visibleClients.length && <p className="muted">Подключений с таким фильтром нет.</p>}</div></section><section className="card client-controls" aria-labelledby="client-controls-title"><p className="section-kicker">SELECTED CLIENT</p><h3 id="client-controls-title">Доступ и токен</h3>{selectedClient ? <><p className="selected-connection-name">{selectedClient.label} · {clientKindLabel(selectedClient.token_kind)}</p><code className="connection-full-id">{selectedClient.id}</code>{selectedClient.token_kind === "oauth" && <p className="field-help">Это регистрация подключения. Выбранная роль действует после следующего запроса, без перевыпуска его токенов.</p>}<label>Роль выбранного подключения<select aria-label="Роль выбранного подключения" value={selectedRole} onChange={(event) => setSelectedRole(event.target.value)}><option value="client">Клиент — выполнение по профилю</option><option value="admin">Администратор — управление и токены</option><option value="owner">Владелец — полное управление</option></select></label><div className="button-row"><button className="button secondary" type="button" onClick={() => void saveRole()} disabled={mutating || !canManage}>Сохранить роль</button><button className="button secondary" type="button" onClick={() => void revealToken()} disabled={mutating || !canReveal}>Показать сохранённый токен</button></div><label>Профиль для выбранного клиента<select value={selectedProfileId} onChange={(event) => setSelectedProfileId(event.target.value)}><option value="">Без профиля</option>{profiles.map((profile) => <option value={profile.id} key={profile.id}>{profile.name || profile.id}</option>)}</select></label><div className="button-row"><button className="button secondary" type="button" onClick={() => void bindProfile()} disabled={!selectedProfileId || mutating || !canManage}>Привязать профиль</button><button className="button secondary" type="button" onClick={() => void unbindProfile()} disabled={!selectedClient.profile_id || mutating || !canManage}>Снять привязку</button></div><div className="button-row"><button className="button secondary" type="button" onClick={() => void rotateToken()} disabled={!supportsManagedActions || mutating}>Ротировать токен</button><button className="button danger" type="button" onClick={() => void revokeToken()} disabled={!supportsManagedActions || mutating}>Отозвать токен</button></div>{!supportsManagedActions && <p className="field-help">OAuth сам обновляет свои токены: роль и профиль меняются здесь, без ротации. Для служебных ключей недоступны неподдерживаемые действия.</p>}</> : <p className="muted">Выберите клиента из инвентаря.</p>}</section><section className="card token-issue-card" aria-labelledby="issue-title"><p className="section-kicker">MANAGED JWT</p><h3 id="issue-title">Выдать managed token</h3><p className="muted">Значение появится здесь и в разделе «Токены и подключение».</p><label>Профиль нового подключения<select aria-label="Профиль нового подключения" value={issueProfileId} onChange={(event) => setIssueProfileId(event.target.value)}><option value="">Без профиля, права из токена</option>{profiles.map((profile) => <option value={profile.id} key={profile.id}>{profile.name || profile.id}</option>)}</select></label><label>Роль нового подключения<select aria-label="Роль нового подключения" value={issueRole} onChange={(event) => { setIssueRole(event.target.value); if (event.target.value !== "client") setAccessMode("full"); }}><option value="client">Клиент</option><option value="admin">Администратор</option><option value="owner">Владелец</option></select></label><p className="field-help">Роль администратора разрешает управление через ИИ и просмотр токенов. Профиль может дополнительно ограничить её; полный режим выполнения сам по себе не даёт роль администратора.</p><label>Client ID<input value={clientId} onChange={(event) => setClientId(event.target.value)} /></label><label>Срок действия, дней<input type="number" min="1" max="3650" value={ttlDays} onChange={(event) => setTtlDays(event.target.value)} /></label><label>Режим доступа<select value={accessMode} onChange={(event) => setAccessMode(event.target.value as AccessMode)}><option value="readonly">Только чтение</option><option value="full">Полный доступ</option></select></label><button className="button primary" type="button" onClick={() => void issueToken()} disabled={!clientId.trim() || issuing}>{issuing ? "Выдаём…" : "Выдать managed token"}</button></section></div>}
      </div>
    </>
  );
}

type WebhookRouteSummary = {
  id: string;
  kind: "mcp" | "prompt" | "shell";
  target: string;
  tool?: string;
  action_count: number;
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
  additionalActions: WebhookActionDraft[];
  callbackUrl: string;
  callbackAuthMode: "none" | "token" | "hmac";
  callbackSecret: string;
};

type WebhookActionDraft = {
  kind: "mcp" | "prompt" | "shell";
  target: string;
  approvalMode: "" | "ask_before_write" | "bounded_autonomous";
  tool: string;
  argumentsJson: string;
  prompt: string;
  promptArg: string;
  command: string;
  cwd: string;
  delaySeconds: string;
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

type WebhookJobSummary = Omit<WebhookJob, "started_at" | "result">;

type WebhookJobOverview = {
  jobs?: WebhookJobSummary[];
  latest_by_route?: Record<string, WebhookJobSummary>;
};

function webhookStatusLabel(status?: string): string {
  if (status === "completed") return "Готово";
  if (status === "failed") return "Ошибка";
  if (status === "running") return "Выполняется";
  if (status === "accepted" || status === "queued") return "Принято";
  return status || "Не запускался";
}

function webhookActionLabel(kind: WebhookRouteSummary["kind"]): string {
  if (kind === "shell") return "Команда на сервере";
  if (kind === "prompt") return "AI-запрос";
  return "Действие MCP";
}

function formatWebhookTime(value?: string): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("ru-RU", { dateStyle: "short", timeStyle: "short" });
}

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
  additionalActions: [],
  callbackUrl: "",
  callbackAuthMode: "none",
  callbackSecret: "",
});

const emptyWebhookAction = (): WebhookActionDraft => ({
  kind: "mcp",
  target: "",
  approvalMode: "",
  tool: "",
  argumentsJson: "{}",
  prompt: "",
  promptArg: "",
  command: "",
  cwd: "",
  delaySeconds: "0",
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
    action_count: typeof route.action_count === "number" && route.action_count > 0 ? route.action_count : 1,
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
  const [editorOpen, setEditorOpen] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [mutating, setMutating] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [jobId, setJobId] = useState("");
  const [job, setJob] = useState<WebhookJob | null>(null);
  const [jobMessage, setJobMessage] = useState<string | null>(null);
  const [checkingJob, setCheckingJob] = useState(false);
  const [recentJobs, setRecentJobs] = useState<WebhookJobSummary[]>([]);
  const [latestByRoute, setLatestByRoute] = useState<Record<string, WebhookJobSummary>>({});
  const [showHowItWorks, setShowHowItWorks] = useState(false);

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

  async function loadJobOverview(): Promise<void> {
    try {
      const response = await webhookRequest<WebhookJobOverview>("/admin/api/webhook-jobs");
      setRecentJobs(response.jobs ?? []);
      setLatestByRoute(response.latest_by_route ?? {});
    } catch {
      // Route management still works if historical job summaries are unavailable.
    }
  }

  useEffect(() => { void loadRoutes(); void loadJobOverview(); }, []);

  function startCreate(): void {
    setEditorOpen(true);
    setEditingId(null);
    setDraft(emptyWebhookRoute());
    setConfirmDelete(false);
    setMessage(null);
  }

  function startEdit(route: WebhookRouteSummary): void {
    setEditorOpen(true);
    setEditingId(route.id);
    setDraft({
      ...emptyWebhookRoute(),
      id: route.id,
      authMode: route.auth_mode,
      kind: route.kind,
      target: route.target,
      tool: route.tool ?? "",
      callbackAuthMode: route.callback_configured ? "hmac" : "none",
      additionalActions: Array.from({ length: Math.max(0, route.action_count - 1) }, emptyWebhookAction),
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

    const additionalActions = draft.additionalActions.map((additional, index) => {
      if (!additional.target.trim()) throw new Error(`Укажите цель для шага ${index + 2}.`);
      const action: Record<string, unknown> = { kind: additional.kind, target: additional.target.trim() };
      if (additional.approvalMode) action.approval_mode = additional.approvalMode;
      const delay = Number(additional.delaySeconds);
      if (!Number.isFinite(delay) || delay < 0) throw new Error(`Пауза перед шагом ${index + 2} должна быть неотрицательным числом.`);
      if (delay > 0) action.delay_seconds = delay;
      if (additional.kind === "shell") {
        if (!additional.command.trim()) throw new Error(`Для шага ${index + 2} укажите Shell-команду.`);
        action.command = additional.command;
        if (additional.cwd.trim()) action.cwd = additional.cwd.trim();
      } else {
        if (!additional.tool.trim()) throw new Error(`Для шага ${index + 2} укажите инструмент.`);
        action.tool = additional.tool.trim();
        if (additional.argumentsJson.trim()) {
          const parsed = JSON.parse(additional.argumentsJson) as unknown;
          if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) throw new Error(`Аргументы шага ${index + 2} должны быть JSON-объектом.`);
          action.arguments = parsed;
        }
        if (additional.kind === "prompt") {
          if (!additional.prompt.trim()) throw new Error(`Для шага ${index + 2} укажите шаблон prompt.`);
          action.prompt = additional.prompt;
          if (additional.promptArg.trim()) action.prompt_arg = additional.promptArg.trim();
        }
      }
      return action;
    });

    const route: Record<string, unknown> = { id, ...(additionalActions.length ? { actions: [action, ...additionalActions] } : { action }) };
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
      setEditorOpen(false);
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
      void loadJobOverview();
    } catch (error) {
      setJobMessage(error instanceof Error ? error.message : "Не удалось получить задание.");
    } finally {
      setCheckingJob(false);
    }
  }

  function startPreset(kind: "notify" | "shell"): void {
    setEditorOpen(true);
    setEditingId(null);
    setConfirmDelete(false);
    setMessage(null);
    if (kind === "notify") {
      setDraft({
        ...emptyWebhookRoute(),
        kind: "mcp",
        target: "mcp:shell:admin-server-100:Notify",
        tool: "send_message",
        argumentsJson: JSON.stringify({ title: "Событие", message: "{{event.message}}" }, null, 2),
      });
      return;
    }
    setDraft({ ...emptyWebhookRoute(), kind: "shell", target: "shell:admin-server-100", approvalMode: "ask_before_write" });
  }

  async function copyEndpoint(routeID: string): Promise<void> {
    const endpoint = `${window.location.origin}/webhooks/v1/${routeID}`;
    try {
      await navigator.clipboard.writeText(endpoint);
      setMessage("Адрес автоматизации скопирован");
    } catch {
      setMessage(endpoint);
    }
  }

  const editingRoute = editingId ? routes.find((route) => route.id === editingId) : null;
  const resultEntries = job?.result ? visibleJobResult(job.result) : [];

  return (
    <>
      <header className="topbar"><div><span className="eyebrow">АВТОМАТИЗАЦИЯ</span><h1>Автоматизация</h1></div><button className="button primary topbar-action" type="button" onClick={startCreate}>Создать автоматизацию</button></header>
      <div className="content-wrap">
        <section className="automation-intro card">
          <div className="automation-intro-copy"><p className="section-kicker">КАК ЭТО РАБОТАЕТ</p><h2>Событие приходит — GPTAdmin выполняет заранее выбранное действие</h2><p>Например: мониторинг сообщает о проблеме → GPTAdmin отправляет уведомление или запускает проверку на сервере. Внешний сервис не может сам выбрать произвольную команду: действие закреплено в автоматизации заранее.</p></div>
          <div className="automation-flow" aria-label="Схема автоматизации"><span>1. Событие</span><b>→</b><span>2. Проверка секрета</span><b>→</b><span>3. Действие</span><b>→</b><span>4. Результат</span></div>
          <div className="automation-quick-actions"><button className="button secondary" type="button" onClick={() => startPreset("notify")}>Шаблон: отправить сообщение</button><button className="button secondary" type="button" onClick={() => startPreset("shell")}>Шаблон: команда на сервере</button><button className="text-button" type="button" onClick={() => setShowHowItWorks((value) => !value)}>{showHowItWorks ? "Скрыть подсказки" : "Показать подробную подсказку"}</button></div>
          {showHowItWorks && <div className="automation-help"><p><strong>Адрес.</strong> После создания получится URL вида <code>/webhooks/v1/имя</code>. Его вызывает внешний сервис методом POST.</p><p><strong>Секрет.</strong> Он нужен, чтобы посторонний человек не смог запустить автоматизацию. Для простых интеграций можно использовать Bearer token; HMAC подходит системам, которые умеют подписывать запросы.</p><p><strong>Действие.</strong> MCP вызывает один заранее выбранный инструмент; AI-запрос передаёт событие в выбранный инструмент; команда на сервере запускает фиксированную команду.</p><p><strong>Проверка.</strong> Ниже рядом с каждой автоматизацией показан последний запуск и его результат.</p></div>}
        </section>

        <section className="intro automation-summary"><div><p className="section-kicker">АКТИВНЫЕ ПРАВИЛА</p><h2>{routes.length ? `${routes.length} автоматизаций настроено` : "Автоматизаций пока нет"}</h2><p className="lede">Создание правила ничего не запускает само по себе: оно ждёт входящее событие по своему адресу.</p></div><div className={`data-badge state-${loadState}`} role="status"><span className="state-dot" aria-hidden="true" />{loadState === "loading" ? "Загрузка" : stateLabel(loadState)}</div></section>

        {message && !editorOpen && loadState !== "error" && <div className={message.includes("создан") || message.includes("заменён") || message.includes("удалён") ? "state-panel card standalone-state" : "state-panel card standalone-state state-error"} role="status">{message}</div>}

        {loadState === "loading" && <div className="state-panel card standalone-state" role="status"><span className="loader" aria-hidden="true" />Загрузка маршрутов</div>}
        {loadState === "error" && <div className="state-panel card standalone-state state-error" role="alert"><strong>Не удалось загрузить маршруты</strong><span>{message}</span><button className="button secondary" type="button" onClick={() => void loadRoutes()}>Повторить</button></div>}
        {loadState !== "loading" && loadState !== "error" && <section className={`webhook-layout ${editorOpen ? "editor-open" : "compact"}`}>
          <aside className="route-list card" aria-label="Список webhook-маршрутов">
            <div className="list-heading"><div><p className="section-kicker">АВТОМАТИЗАЦИИ</p><h3>Настроенные правила</h3></div><button className="text-button" type="button" onClick={startCreate}>+ Новый</button></div>
            {routes.length === 0 && <div className="route-empty"><strong>Автоматизаций пока нет</strong><span>Начните с готового шаблона выше или создайте правило вручную.</span></div>}
            {routes.map((route) => { const latest = latestByRoute[route.id]; return <article className={`route-row automation-route-row ${editingId === route.id ? "selected" : ""}`} key={route.id}><div className="automation-route-main"><div className="automation-route-title"><strong>{route.id}</strong>{latest ? <span className={`automation-run-state state-${latest.status}`}>{webhookStatusLabel(latest.status)}</span> : <span className="automation-run-state state-never">Не запускалась</span>}</div><span>{webhookActionLabel(route.kind)} · {route.action_count === 1 ? "1 шаг" : `${route.action_count} шага`}</span><small>{route.target}</small>{latest && <small>Последний запуск: {formatWebhookTime(latest.created_at)}{latest.error ? ` · ${latest.error}` : ""}</small>}</div><div className="automation-route-actions"><button className="text-button" type="button" onClick={() => void copyEndpoint(route.id)}>Скопировать адрес</button><button className="text-button" type="button" aria-label={`Изменить ${route.id}`} onClick={() => startEdit(route)}>Изменить</button></div></article>; })}
          </aside>

          {editorOpen && <section className="card route-editor" aria-labelledby="route-editor-title">
            <div className="card-heading"><div><p className="section-kicker">{editingId ? "РЕДАКТИРОВАНИЕ" : "НОВАЯ АВТОМАТИЗАЦИЯ"}</p><h3 id="route-editor-title">{editingId ? `Изменить ${editingId}` : "Новая автоматизация"}</h3></div><span className="chip">Секрет не показывается после сохранения</span></div>
            {message && <div className={message.includes("создан") || message.includes("заменён") || message.includes("удалён") ? "form-message success-text" : "form-message warning-text"} role="status">{message}</div>}
            <form className="route-form" onSubmit={(event) => { event.preventDefault(); void saveRoute(); }}>
              <div className="form-grid">
                <label>Короткое имя<input value={draft.id} disabled={editingId !== null} onChange={(event) => setDraft({ ...draft, id: event.target.value })} /></label>
                <label>Защита входящего события<select value={draft.authMode} onChange={(event) => setDraft({ ...draft, authMode: event.target.value as WebhookRouteDraft["authMode"] })}><option value="hmac">Подпись HMAC (надёжнее)</option><option value="token">Секретный токен (проще)</option></select></label>
                <label>Секрет для входящего запроса<input aria-label="Секрет для входящего запроса" type="password" autoComplete="new-password" value={draft.secret} onChange={(event) => setDraft({ ...draft, secret: event.target.value })} /><small>Сохраните его во внешней системе: GPTAdmin больше не покажет это значение.</small></label>
                {draft.authMode === "hmac" && <label>Версия подписи<select value={draft.signatureVersion} onChange={(event) => setDraft({ ...draft, signatureVersion: event.target.value as WebhookRouteDraft["signatureVersion"] })}><option value="v2">v2</option><option value="v1">v1</option></select></label>}
                {draft.authMode === "hmac" && <label>Допустимое отклонение, секунд<input type="number" min="1" value={draft.maxSkewSeconds} onChange={(event) => setDraft({ ...draft, maxSkewSeconds: event.target.value })} /></label>}
                <label>Что сделать<select value={draft.kind} onChange={(event) => { const kind = event.target.value as WebhookRouteDraft["kind"]; setDraft({ ...draft, kind, target: kind !== draft.kind ? "" : draft.target }); }}><option value="mcp">Вызвать действие MCP</option><option value="prompt">Передать событие AI-инструменту</option><option value="shell">Запустить фиксированную команду</option></select></label>
                <label>Где выполнить<input value={draft.target} onChange={(event) => setDraft({ ...draft, target: event.target.value })} placeholder="например: mcp:shell:admin-server-100:Notify" /></label>
                <label>Подтверждение опасных действий<select value={draft.approvalMode} onChange={(event) => setDraft({ ...draft, approvalMode: event.target.value as WebhookRouteDraft["approvalMode"] })}><option value="">Обычные правила безопасности</option><option value="ask_before_write">Спросить перед изменением</option><option value="bounded_autonomous">Разрешить ограниченно автоматически</option></select></label>
              </div>
              {draft.kind === "shell" ? <div className="form-grid"><label>Команда<input value={draft.command} onChange={(event) => setDraft({ ...draft, command: event.target.value })} /></label><label>Рабочий каталог<input value={draft.cwd} onChange={(event) => setDraft({ ...draft, cwd: event.target.value })} /></label></div> : <><div className="form-grid"><label>Инструмент<input value={draft.tool} onChange={(event) => setDraft({ ...draft, tool: event.target.value })} /></label>{draft.kind === "prompt" && <label>Аргумент prompt<input value={draft.promptArg} onChange={(event) => setDraft({ ...draft, promptArg: event.target.value })} placeholder="message" /></label>}</div><label>Аргументы JSON<textarea className="short-textarea" value={draft.argumentsJson} onChange={(event) => setDraft({ ...draft, argumentsJson: event.target.value })} spellCheck="false" /></label>{draft.kind === "prompt" && <label>Шаблон сообщения<textarea className="short-textarea" value={draft.prompt} onChange={(event) => setDraft({ ...draft, prompt: event.target.value })} /></label>}</>}
              <section className="action-builder" aria-label="Последовательность действий">
                <div className="card-heading"><div><p className="section-kicker">ORDERED FLOW</p><h3>Следующие шаги</h3><p className="muted">Каждый шаг знает только свои параметры. Пауза задаётся перед этим шагом.</p></div><button className="button secondary" type="button" onClick={() => setDraft({ ...draft, additionalActions: [...draft.additionalActions, emptyWebhookAction()] })}>+ Добавить шаг</button></div>
                {draft.additionalActions.length === 0 && <p className="field-help">После первого действия пока ничего не выполняется.</p>}
                {draft.additionalActions.map((action, index) => <article className="action-card" key={index}>
                  <div className="action-card-heading"><strong>Шаг {index + 2}</strong><button className="text-button" type="button" onClick={() => setDraft({ ...draft, additionalActions: draft.additionalActions.filter((_, actionIndex) => actionIndex !== index) })}>Удалить шаг</button></div>
                  <div className="form-grid">
                    <label>Что сделать<select value={action.kind} onChange={(event) => { const next = event.target.value as WebhookActionDraft["kind"]; setDraft({ ...draft, additionalActions: draft.additionalActions.map((item, actionIndex) => actionIndex === index ? { ...item, kind: next } : item) }); }}><option value="mcp">Вызвать действие MCP</option><option value="prompt">Передать событие AI-инструменту</option><option value="shell">Запустить фиксированную команду</option></select></label>
                    <label>Пауза перед шагом, секунд<input type="number" min="0" step="0.1" value={action.delaySeconds} onChange={(event) => setDraft({ ...draft, additionalActions: draft.additionalActions.map((item, actionIndex) => actionIndex === index ? { ...item, delaySeconds: event.target.value } : item) })} /></label>
                    <label>Где выполнить<input value={action.target} onChange={(event) => setDraft({ ...draft, additionalActions: draft.additionalActions.map((item, actionIndex) => actionIndex === index ? { ...item, target: event.target.value } : item) })} placeholder="mcp:shell:...:Notify" /></label>
                    <label>Подтверждение опасных действий<select value={action.approvalMode} onChange={(event) => setDraft({ ...draft, additionalActions: draft.additionalActions.map((item, actionIndex) => actionIndex === index ? { ...item, approvalMode: event.target.value as WebhookActionDraft["approvalMode"] } : item) })}><option value="">Обычные правила безопасности</option><option value="ask_before_write">Спросить перед изменением</option><option value="bounded_autonomous">Разрешить ограниченно автоматически</option></select></label>
                  </div>
                  {action.kind === "shell" ? <div className="form-grid"><label>Команда<input value={action.command} onChange={(event) => setDraft({ ...draft, additionalActions: draft.additionalActions.map((item, actionIndex) => actionIndex === index ? { ...item, command: event.target.value } : item) })} /></label><label>Рабочий каталог<input value={action.cwd} onChange={(event) => setDraft({ ...draft, additionalActions: draft.additionalActions.map((item, actionIndex) => actionIndex === index ? { ...item, cwd: event.target.value } : item) })} /></label></div> : <><div className="form-grid"><label>Инструмент<input value={action.tool} onChange={(event) => setDraft({ ...draft, additionalActions: draft.additionalActions.map((item, actionIndex) => actionIndex === index ? { ...item, tool: event.target.value } : item) })} /></label>{action.kind === "prompt" && <label>Аргумент prompt<input value={action.promptArg} onChange={(event) => setDraft({ ...draft, additionalActions: draft.additionalActions.map((item, actionIndex) => actionIndex === index ? { ...item, promptArg: event.target.value } : item) })} /></label>}</div><label>Аргументы JSON<textarea className="short-textarea" value={action.argumentsJson} onChange={(event) => setDraft({ ...draft, additionalActions: draft.additionalActions.map((item, actionIndex) => actionIndex === index ? { ...item, argumentsJson: event.target.value } : item) })} spellCheck="false" /></label>{action.kind === "prompt" && <label>Шаблон сообщения<textarea className="short-textarea" value={action.prompt} onChange={(event) => setDraft({ ...draft, additionalActions: draft.additionalActions.map((item, actionIndex) => actionIndex === index ? { ...item, prompt: event.target.value } : item) })} /></label>}</>}
                </article>)}
              </section>
              <fieldset className="callback-fieldset"><legend>Callback (необязательно)</legend><div className="form-grid"><label>URL callback<input type="url" value={draft.callbackUrl} onChange={(event) => setDraft({ ...draft, callbackUrl: event.target.value })} /></label><label>Авторизация callback<select value={draft.callbackAuthMode} onChange={(event) => setDraft({ ...draft, callbackAuthMode: event.target.value as WebhookRouteDraft["callbackAuthMode"] })}><option value="none">Без авторизации</option><option value="hmac">HMAC</option><option value="token">Bearer token</option></select></label>{draft.callbackAuthMode !== "none" && <label>Секрет callback<input type="password" autoComplete="new-password" value={draft.callbackSecret} onChange={(event) => setDraft({ ...draft, callbackSecret: event.target.value })} /></label>}</div>{editingRoute?.callback_configured && <p className="field-help">Текущий callback скрыт. Чтобы сохранить его при замене, повторно заполните URL и авторизацию.</p>}</fieldset>
              <div className="route-actions"><button className="button secondary" type="button" onClick={() => { setEditorOpen(false); setEditingId(null); setConfirmDelete(false); setMessage(null); }}>Закрыть</button><button className="button primary" type="submit" disabled={mutating}>{mutating ? "Сохраняем…" : editingId ? "Заменить маршрут" : "Создать маршрут"}</button>{editingId && <button className="button danger" type="button" onClick={() => setConfirmDelete(true)} disabled={mutating}>Удалить маршрут</button>}</div>
            </form>
            {confirmDelete && editingId && <div className="delete-confirmation" role="alertdialog" aria-modal="true" aria-labelledby="delete-route-title" onKeyDown={(event) => { if (event.key === "Escape") setConfirmDelete(false); }}><strong id="delete-route-title">Удалить маршрут {editingId}?</strong><p>Маршрут перестанет принимать новые события. Это действие требует явного подтверждения.</p><div className="button-row"><button className="button secondary" type="button" onClick={() => setConfirmDelete(false)}>Отмена</button><button className="button danger" type="button" autoFocus onClick={() => void deleteRoute()} disabled={mutating}>Подтвердить удаление</button></div></div>}
          </section>}

          <section className="card job-inspector" aria-labelledby="job-inspector-title">
            <div><p className="section-kicker">ИСТОРИЯ</p><h3 id="job-inspector-title">Последние запуски</h3><p className="muted">Здесь видно, срабатывают ли автоматизации на практике. Для подробностей можно открыть запуск по его ID.</p></div>
            {recentJobs.length > 0 ? <div className="automation-recent-jobs">{recentJobs.slice(0, 8).map((item) => <button type="button" className="automation-job-row" key={item.job_id} onClick={() => { setJobId(item.job_id); setJob(null); }}><span className={`automation-run-state state-${item.status}`}>{webhookStatusLabel(item.status)}</span><span><strong>{item.route_id}</strong><small>{formatWebhookTime(item.created_at)}{item.error ? ` · ${item.error}` : ""}</small></span></button>)}</div> : <div className="route-empty"><strong>Запусков пока нет</strong><span>Когда внешний сервис вызовет автоматизацию, результат появится здесь.</span></div>}
            <details className="automation-job-lookup"><summary>Открыть запуск по ID</summary><form className="job-search" onSubmit={(event) => { event.preventDefault(); void inspectJob(); }}><label>ID запуска<input value={jobId} onChange={(event) => setJobId(event.target.value)} /></label><button className="button secondary" type="submit" disabled={!jobId.trim() || checkingJob}>{checkingJob ? "Проверяем…" : "Открыть"}</button></form></details>
            {jobMessage && <div className="state-panel state-error compact" role="alert"><strong>Не удалось получить задание</strong><span>{jobMessage}</span><button className="button secondary" type="button" onClick={() => void inspectJob()}>Повторить</button></div>}
            {job && <div className="job-result" role="status"><dl><div><dt>ID</dt><dd>{job.job_id}</dd></div><div><dt>Маршрут</dt><dd>{job.route_id}</dd></div><div><dt>Состояние</dt><dd>{webhookStatusLabel(job.status)}</dd></div>{job.created_at && <div><dt>Создано</dt><dd>{formatUpdated(job.created_at)}</dd></div>}{job.started_at && <div><dt>Запущено</dt><dd>{formatUpdated(job.started_at)}</dd></div>}{job.completed_at && <div><dt>Завершено</dt><dd>{formatUpdated(job.completed_at)}</dd></div>}{job.callback_status && <div><dt>Callback</dt><dd>{job.callback_status}</dd></div>}{job.error && <div><dt>Ошибка</dt><dd>{job.error}</dd></div>}</dl>{resultEntries.length > 0 && <div className="safe-result"><strong>Результат</strong><dl>{resultEntries.map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl></div>}</div>}
          </section>
        </section>}
      </div>
    </>
  );
}

function AuthScreen({ token }: { token: TokenResponse | null }) {
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

  return <><header className="topbar"><div><span className="eyebrow">AUTHENTICATION / 04</span><h1>Авторизация</h1></div></header><div className="content-wrap"><section className="intro"><div><p className="section-kicker">OAUTH CLIENT SECRET</p><h2>Управление доступом Hub</h2><p className="lede">Токены подключения и управление OAuth. Ротация — отдельное действие, а не условие просмотра.</p></div></section><section className="card auth-card"><h3>Выданный токен</h3>{token ? <><p>{token.client_id || token.token_id}</p><textarea aria-label="Выданный токен подключения" readOnly value={token.access_token} /><p className="muted">Сохраняется при переключении разделов. После перезагрузки откройте «Клиенты» → «Показать сохранённый токен».</p></> : <p>Откройте «Клиенты», выберите подключение и нажмите «Показать сохранённый токен». Просмотр не ротирует подключение.</p>}</section><section className="card auth-card"><h3>OAuth secret</h3><p className="muted">Используйте ротацию только при плановом обновлении или подозрении на компрометацию.</p><button className="button primary" type="button" onClick={() => void rotate()} disabled={rotating}>{rotating ? "Обновляем…" : "Ротировать OAuth secret"}</button>{message && <p className="success-text" role="status">{message}</p>}</section></div></>;
}

function CapabilitiesScreen() {
  const [items, setItems] = useState<VirtualMCP[]>([]);
  const [loadState, setLoadState] = useState<LoadState>("loading");
  const [message, setMessage] = useState<string | null>(null);
  const [changing, setChanging] = useState<string | null>(null);
  const [settingsSchema, setSettingsSchema] = useState<HubSettingDefinition[]>([]);
  const [settingsValues, setSettingsValues] = useState<Record<string, unknown>>({});
  const [settingsDraft, setSettingsDraft] = useState<Record<string, unknown>>({});
  const [savingSettings, setSavingSettings] = useState(false);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [settingsHistory, setSettingsHistory] = useState<SettingsRevision[]>([]);
  const [cleanupCandidates, setCleanupCandidates] = useState<CleanupCandidate[]>([]);
  const [tombstones, setTombstones] = useState<AgentTombstone[]>([]);
  const [maintenanceBusy, setMaintenanceBusy] = useState(false);
  const [diagnoseSnapshot, setDiagnoseSnapshot] = useState<Record<string, unknown> | null>(null);

  async function load(): Promise<void> {
    setLoadState("loading");
    setMessage(null);
    try {
      const [virtualMCPs, hubSettings, history, maintenance, deleted] = await Promise.all([getVirtualMCPs(), getHubSettings(), getSettingsHistory(), getRegistryMaintenance(), getAgentTombstones()]);
      setItems(virtualMCPs);
      const ordered = [...(hubSettings.schema.settings || [])].sort((a, b) => (a.order || 0) - (b.order || 0) || a.key.localeCompare(b.key));
      setSettingsSchema(ordered);
      setSettingsValues(hubSettings.settings);
      setSettingsDraft(hubSettings.settings);
      setSettingsHistory(history.history.slice().reverse());
      setCleanupCandidates(maintenance.candidates);
      setTombstones(deleted.slice().reverse());
      setLoadState("ready");
    } catch (error) {
      setLoadState("error");
      setMessage(error instanceof Error ? error.message : "Не удалось загрузить Hub settings.");
    }
  }

  useEffect(() => { void load(); }, []);

  function normalizeSetting(def: HubSettingDefinition, value: unknown): unknown {
    if (def.type === "integer") {
      const number = Number(value);
      if (!Number.isInteger(number)) throw new Error(`${def.title}: требуется целое число.`);
      if (def.minimum !== undefined && number < def.minimum) throw new Error(`${def.title}: минимум ${def.minimum}.`);
      if (def.maximum !== undefined && number > def.maximum) throw new Error(`${def.title}: максимум ${def.maximum}.`);
      return number;
    }
    if (def.type === "boolean") return Boolean(value);
    if (def.type === "enum") {
      const text = String(value);
      if (def.options?.length && !def.options.includes(text)) throw new Error(`${def.title}: недопустимое значение.`);
      return text;
    }
    return String(value ?? "");
  }

  async function saveSettings(): Promise<void> {
    const patch: Record<string, unknown> = {};
    try {
      for (const def of settingsSchema) {
        if (def.read_only) continue;
        const next = normalizeSetting(def, settingsDraft[def.key] ?? def.default);
        if (JSON.stringify(next) !== JSON.stringify(settingsValues[def.key] ?? def.default)) patch[def.key] = next;
      }
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Некорректное значение настройки.");
      return;
    }
    if (Object.keys(patch).length === 0) { setMessage("Изменений нет."); return; }
    const dangerous = settingsSchema.filter((def) => def.dangerous && def.key in patch);
    if (dangerous.length && !window.confirm(`Изменяются потенциально опасные настройки: ${dangerous.map((item) => item.title).join(", ")}. Продолжить?`)) return;
    setSavingSettings(true); setMessage(null);
    try {
      const settings = await setHubSettings(patch);
      setSettingsValues(settings);
      setSettingsDraft(settings);
      const restartRequired = settingsSchema.filter((def) => def.restart_required && def.key in patch);
      setMessage(restartRequired.length ? `Сохранено. Требуется restart: ${restartRequired.map((item) => item.title).join(", ")}.` : "Настройки сохранены в Hub.");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Не удалось сохранить настройки Hub.");
    } finally { setSavingSettings(false); }
  }

  function settingControl(def: HubSettingDefinition) {
    const value = settingsDraft[def.key] ?? def.default;
    const disabled = savingSettings || def.read_only;
    if (def.type === "boolean") return <input type="checkbox" checked={Boolean(value)} disabled={disabled} onChange={(event) => setSettingsDraft((current) => ({ ...current, [def.key]: event.target.checked }))} />;
    if (def.type === "enum") return <select value={String(value ?? "")} disabled={disabled} onChange={(event) => setSettingsDraft((current) => ({ ...current, [def.key]: event.target.value }))}>{(def.options || []).map((option) => <option key={option} value={option}>{option}</option>)}</select>;
    if (def.type === "integer") return <input type="number" min={def.minimum} max={def.maximum} value={Number(value ?? def.default)} disabled={disabled} onChange={(event) => setSettingsDraft((current) => ({ ...current, [def.key]: event.target.valueAsNumber }))} />;
    return <input type={def.secret ? "password" : "text"} value={String(value ?? "")} disabled={disabled} onChange={(event) => setSettingsDraft((current) => ({ ...current, [def.key]: event.target.value }))} />;
  }

  const categories = Array.from(new Set(settingsSchema.filter((item) => showAdvanced || !item.advanced).map((item) => item.category)));

  async function refreshDiagnose(): Promise<void> {
    setMaintenanceBusy(true); setMessage(null);
    try { setDiagnoseSnapshot(await getHubDiagnose()); }
    catch (error) { setMessage(error instanceof Error ? error.message : "Diagnose failed."); }
    finally { setMaintenanceBusy(false); }
  }
  async function scheduleHandover(): Promise<void> {
    if (!window.confirm("Запустить zero-downtime handover primary Hub?")) return;
    setMaintenanceBusy(true); setMessage(null);
    try { const result = await startHubHandover(); setMessage(`Handover запланирован: ${String(result.unit || "accepted")}`); }
    catch (error) { setMessage(error instanceof Error ? error.message : "Handover failed."); }
    finally { setMaintenanceBusy(false); }
  }

  async function rollbackRevision(revision: number): Promise<void> {
    if (!window.confirm(`Откатить Hub settings к revision ${revision}?`)) return;
    setMaintenanceBusy(true); setMessage(null);
    try { const settings = await rollbackHubSettings(revision); setSettingsValues(settings); setSettingsDraft(settings); await load(); setMessage(`Settings откатаны к revision ${revision}.`); }
    catch (error) { setMessage(error instanceof Error ? error.message : "Rollback failed."); }
    finally { setMaintenanceBusy(false); }
  }

  async function cleanupNow(): Promise<void> {
    const eligible = cleanupCandidates.filter((item) => item.eligible);
    if (!eligible.length) { setMessage("Нет MCP, подходящих под cleanup policy."); return; }
    if (!window.confirm(`Удалить ${eligible.length} stale MCP? Будут созданы tombstones.`)) return;
    setMaintenanceBusy(true); setMessage(null);
    try { const removed = await runRegistryCleanup(); await load(); setMessage(`Удалено stale MCP: ${removed.length}.`); }
    catch (error) { setMessage(error instanceof Error ? error.message : "Cleanup failed."); }
    finally { setMaintenanceBusy(false); }
  }

  async function updateCleanupPolicy(item: CleanupCandidate, patch: Partial<CleanupCandidate["policy"]>): Promise<void> {
    setMaintenanceBusy(true); setMessage(null);
    try { await setAgentCleanupPolicy(item.agent_id, patch); await load(); setMessage(`Policy сохранена для ${item.agent_id}.`); }
    catch (error) { setMessage(error instanceof Error ? error.message : "Policy update failed."); }
    finally { setMaintenanceBusy(false); }
  }

  async function toggle(item: VirtualMCP): Promise<void> {
    if (changing) return;
    setChanging(item.id); setMessage(null);
    try {
      await setVirtualMCP(item.id, !item.enabled);
      setItems((current) => current.map((entry) => entry.id === item.id ? { ...entry, enabled: !entry.enabled } : entry));
    } catch (error) { setMessage(error instanceof Error ? error.message : "Не удалось изменить виртуальный MCP."); }
    finally { setChanging(null); }
  }

  return <><header className="topbar"><div><span className="eyebrow">OPTIONAL CAPABILITIES / 05</span><h1>Виртуальные MCP</h1></div><button className="button secondary topbar-action" type="button" onClick={() => void load()}>Обновить</button></header><div className="content-wrap"><section className="intro"><div><p className="section-kicker">HUB-DRIVEN SETTINGS</p><h2>Настройки из Hub schema</h2><p className="lede">Форма строится автоматически из settings_schema. Новая настройка в Hub появляется здесь без отдельной правки React.</p></div><div className={`data-badge state-${loadState}`} role="status"><span className="state-dot" aria-hidden="true" />{stateLabel(loadState)}</div></section>
  <section className="card auth-card"><div className="card-heading"><div><p className="section-kicker">UNIFIED SETTINGS REGISTRY</p><h3>Hub settings</h3></div><label className="muted"><input type="checkbox" checked={showAdvanced} onChange={(event) => setShowAdvanced(event.target.checked)} /> Расширенные</label></div>
    {categories.map((category) => <div key={category} style={{marginTop:18}}><h4>{category.replaceAll("_", " ")}</h4>{settingsSchema.filter((def) => def.category === category && (showAdvanced || !def.advanced)).map((def) => <div key={def.key} className="client-row" style={{alignItems:"center"}}><div style={{minWidth:0}}><strong>{def.title || def.key}</strong><span>{def.description}</span><span className="muted">{def.key}{def.unit ? ` · ${def.unit}` : ""}{def.restart_required ? " · restart required" : ""}{def.dangerous ? " · dangerous" : ""}{def.read_only ? " · read only" : ""}</span></div><div style={{minWidth:180}}>{settingControl(def)}</div></div>)}</div>)}
    <button className="button primary" type="button" onClick={() => void saveSettings()} disabled={savingSettings}>{savingSettings ? "Сохраняем…" : "Сохранить настройки"}</button>
  </section>
  <section className="card auth-card"><div className="card-heading"><div><p className="section-kicker">REGISTRY MAINTENANCE</p><h3>Cleanup preview и protection</h3></div><button className="button danger" type="button" disabled={maintenanceBusy || !cleanupCandidates.some((item) => item.eligible)} onClick={() => void cleanupNow()}>Удалить eligible</button></div>
    {cleanupCandidates.length === 0 ? <p className="muted">Stale MCP кандидатов нет.</p> : cleanupCandidates.map((item) => <div className="client-row" key={item.agent_id}><div><strong>{item.name || item.agent_id}</strong><span>{item.agent_id} · age {item.age_days}d / retention {item.retention_days}d · {item.reason}</span></div><div><label><input type="checkbox" checked={Boolean(item.policy?.protected)} disabled={maintenanceBusy} onChange={(event) => void updateCleanupPolicy(item, { protected: event.target.checked })} /> protected</label><label><input type="checkbox" checked={Boolean(item.policy?.never_delete)} disabled={maintenanceBusy} onChange={(event) => void updateCleanupPolicy(item, { never_delete: event.target.checked })} /> never delete</label></div></div>)}
  </section>
  <section className="card auth-card"><div className="card-heading"><div><p className="section-kicker">SETTINGS HISTORY</p><h3>Revisions и rollback</h3></div></div>
    {settingsHistory.length === 0 ? <p className="muted">История появится после первого изменения settings.</p> : settingsHistory.slice(0, 20).map((revision) => <div className="client-row" key={revision.revision}><div><strong>Revision {revision.revision}</strong><span>{revision.time} · {revision.actor} · {revision.source}</span></div><button className="button secondary" type="button" disabled={maintenanceBusy} onClick={() => void rollbackRevision(revision.revision)}>Rollback</button></div>)}
  </section>
  <section className="card auth-card"><div className="card-heading"><div><p className="section-kicker">TOMBSTONES</p><h3>История удалённых MCP</h3></div></div>{tombstones.length === 0 ? <p className="muted">Удалённых MCP пока нет.</p> : tombstones.slice(0,20).map((item) => <div className="client-row" key={`${item.agent_id}-${item.deleted_at}`}><div><strong>{item.name || item.agent_id}</strong><span>{item.agent_id} · {item.delete_reason} · deleted {new Date(item.deleted_at * 1000).toLocaleString()}</span></div></div>)}</section>
  <section className="card auth-card"><div className="card-heading"><div><p className="section-kicker">CONTROL PLANE</p><h3>Diagnose и zero-downtime handover</h3></div><div><button className="button secondary" type="button" disabled={maintenanceBusy} onClick={() => void refreshDiagnose()}>Diagnose</button> <button className="button primary" type="button" disabled={maintenanceBusy} onClick={() => void scheduleHandover()}>Handover</button></div></div>{diagnoseSnapshot ? <pre style={{maxHeight:360,overflow:"auto"}}>{JSON.stringify(diagnoseSnapshot,null,2)}</pre> : <p className="muted">Diagnose собирает Hub/standby health, agents, jobs, settings revision и cleanup state.</p>}</section>
  {message && <div className="state-panel state-error card standalone-state" role="alert">{message}</div>}{loadState === "loading" ? <div className="state-panel card standalone-state" role="status"><span className="loader" aria-hidden="true" />Загрузка capabilities</div> : <section className="clients-grid"><div className="card client-inventory"><div className="card-heading"><div><p className="section-kicker">HUB REGISTRY</p><h3>Доступные MCP</h3></div></div><div className="client-list">{items.map((item) => <article className="client-row" key={item.id}><div><strong>{item.name}</strong><span>{item.enabled ? "Включён и виден клиентам" : "Выключен по умолчанию"}</span></div><dl><div><dt>MCP</dt><dd><code>{item.mcp_path}</code></dd></div><div><dt>Actions</dt><dd><code>{item.actions_path}</code></dd></div></dl><button className={item.enabled ? "button danger" : "button primary"} type="button" onClick={() => void toggle(item)} disabled={changing !== null}>{changing === item.id ? "Сохраняем…" : item.enabled ? "Выключить" : "Включить"}</button></article>)}</div></div><aside className="card client-controls"><p className="section-kicker">ACCESS POLICY</p><h3>Перед выдачей клиенту</h3><p className="muted">Создайте access profile с нужным target и точным набором tools. Отключение MCP убирает его из discovery, но не удаляет его state.</p></aside></section>}</div></>;
}

export default function App() {
  const [issuedToken, setIssuedToken] = useState<TokenResponse | null>(null);
  const [view, setView] = useState<View>(viewFromHash);
  const [adminMode, setAdminMode] = useState(() => localStorage.getItem("gptadmin:advanced") === "1" || isAdvancedView(viewFromHash()));

  useEffect(() => {
    const onHashChange = () => {
      setView(viewFromHash());
    };
    onHashChange();
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  useEffect(() => {
    localStorage.setItem("gptadmin:advanced", adminMode ? "1" : "0");
  }, [adminMode]);

  const activeSection = primaryNavigation.find((section) => section.views.includes(view)) ?? (settingsSection.views.includes(view) ? settingsSection : undefined);
  const contextLinks = activeSection ? (contextualNavigation[activeSection.id] ?? []).filter((item) => adminMode || !isAdvancedView(item.id)) : [];
  const navigate = (next: View) => {
    setView(next);
    const href = `#${next}`;
    if (window.location.hash !== href) window.history.pushState(null, "", href);
  };

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand"><span className="brand-mark" aria-hidden="true">G</span><span>GPTAdmin</span></div>
        <div className="workspace-label">ОПЕРАЦИОННАЯ КОНСОЛЬ</div>
        <nav aria-label="Основная навигация">
          {primaryNavigation.map((item) => {
            const active = item.views.includes(view);
            return <a className={`nav-item ${active ? "active" : ""}`} href={`#${item.target}`} aria-current={active ? "page" : undefined} key={item.id} onClick={(event) => { event.preventDefault(); navigate(item.target); }}><span className="nav-dot" aria-hidden="true" /><span>{item.label}</span></a>;
          })}
        </nav>
        <div className="sidebar-footer">
          <a className={`sidebar-utility ${settingsSection.views.includes(view) ? "active" : ""}`} href="#instructions" onClick={(event) => { event.preventDefault(); navigate("instructions"); }}>Настройки</a>
          <button className={`advanced-mode-toggle ${adminMode ? "active" : ""}`} type="button" aria-pressed={adminMode} onClick={() => {
            const next = !adminMode;
            setAdminMode(next);
            if (!next && isAdvancedView(view)) {
              if (["mcpmanage", "resources", "failover"].includes(view)) navigate("agents");
              else if (view === "tools") navigate("jobs");
              else if (view === "raw") navigate("audit");
              else navigate("instructions");
            }
          }}>{adminMode ? "Скрыть расширенные" : "Для администратора"}</button>
          <a className="sidebar-utility" href="/cloudos/">CloudOS</a>
          <a className="logout-link" href="/admin/logout">Выйти</a>
        </div>
      </aside>
      <main className="main-content">
        {contextLinks.length > 1 && <nav className="context-nav" aria-label={`${activeSection?.label ?? "Раздел"}: подразделы`}>{contextLinks.map((item) => <a key={item.id} href={`#${item.id}`} className={view === item.id ? "active" : ""} aria-current={view === item.id ? "page" : undefined} onClick={(event) => { event.preventDefault(); navigate(item.id); }}>{item.label}</a>)}</nav>}
        {view === "overview" ? <OverviewScreen /> : view === "audit" ? <AuditScreen /> : view === "raw" ? <RawScreen /> : view === "tools" ? <ToolsScreen /> : view === "resources" ? <ResourcesScreen /> : view === "agents" ? <AgentsScreen /> : view === "jobs" ? <JobsScreen /> : view === "mcpmanage" ? <McpManageScreen /> : view === "failover" ? <FailoverScreen /> : view === "security" ? <SecurityScreen /> : view === "operations" ? <AccessOperations /> : view === "instructions" ? <InstructionsScreen /> : view === "profiles" ? <ProfilesScreen /> : view === "clients" ? <ClientsScreen token={issuedToken} setToken={setIssuedToken} /> : view === "webhooks" ? <WebhooksScreen /> : view === "capabilities" ? <CapabilitiesScreen /> : <AuthScreen token={issuedToken} />}
      </main>
    </div>
  );
}
