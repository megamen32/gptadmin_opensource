import { useCallback, useEffect, useMemo, useState } from "react";
import { serverStatusLabel } from "./ui/format";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";

type Server = { server_id?: string; status?: string; meta?: Record<string, unknown> };
type ManagedMcp = { name?: string; server_id?: string; ref?: string; kind?: string; registry_category?: string; parent_server_id?: string; transport?: string; status?: string; health?: Record<string, unknown>; command?: string; url?: string; args?: unknown[]; env?: Record<string, unknown>; enabled?: boolean; stdio_format?: string };

type ManageResponse = { ok?: boolean; response?: { note?: string; servers?: unknown; agent?: unknown; status?: unknown }; servers?: unknown; note?: string; detail?: unknown };
type ListState = "idle" | "loading" | "loaded" | "unavailable";

class HttpResponseError extends Error {
  constructor(message: string, readonly status: number) { super(message); }
}

function inventoryStatusLabel(row: ManagedMcp): string {
  if (row.enabled === false || row.status === "disabled") return "Выключен";
  switch (row.status) {
    case "online": return "В сети";
    case "offline": return "Не в сети";
    case "stale": return "Устарел";
    case "failed": return "Ошибка";
    case "configured": return "Настроен";
    case "awaiting_approval": return "Ожидает подтверждения";
    default: return row.status ? serverStatusLabel(row.status) : "Статус неизвестен";
  }
}

function inventoryCategoryLabel(category?: string): string {
  switch (category) {
    case "hub_core": return "Hub";
    case "hub_virtual_mcp": return "Virtual MCP Hub";
    case "registered_host": return "Зарегистрированный сервер";
    case "registered_runtime_child": return "Дочерний MCP из runtime-реестра";
    case "node_child_config": return "Конфигурация дочернего MCP узла";
    default: return category || "MCP";
  }
}

function statusFromResponse(value: unknown): string | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const response = (value as ManageResponse).response;
  const health = response?.status;
  if (!health || typeof health !== "object" || Array.isArray(health)) return null;
  const status = health as Record<string, unknown>;
  const protocol = status.protocol && typeof status.protocol === "object" ? status.protocol as Record<string, unknown> : {};
  const process = status.process && typeof status.process === "object" ? status.process as Record<string, unknown> : {};
  if (protocol.state === "failed" || process.state === "exited") return "failed";
  if (protocol.state === "ready" || process.state === "running") return "online";
  return "configured";
}

type Draft = { name: string; serverId: string; url: string; command: string; args: string; env: string; runAs: string; backend: string; stdio: string; install: boolean; force: boolean; disabled: boolean };
const emptyDraft: Draft = { name: "", serverId: "", url: "", command: "", args: "[]", env: "{}", runAs: "", backend: "", stdio: "", install: true, force: false, disabled: false };

async function requestJson(path: string, init?: RequestInit): Promise<unknown> {
  const response = await fetch(path, { credentials: "same-origin", ...init, headers: { Accept: "application/json", "Content-Type": "application/json", ...(init?.headers ?? {}) } });
  const text = await response.text();
  let data: unknown = text;
  try { data = text ? JSON.parse(text) : {}; } catch { /* keep text */ }
  if (!response.ok) throw new HttpResponseError(typeof data === "object" && data && "detail" in data ? String((data as { detail?: unknown }).detail) : `HTTP ${response.status}`, response.status);
  return data;
}

function extractServers(value: unknown, target: string): ManagedMcp[] {
  const data = value && typeof value === "object" ? value as ManageResponse : {};
  const servers = data.response?.servers ?? data.servers;
  if (data.ok === false) throw new Error(typeof data.detail === "string" ? data.detail : "Hub отклонил запрос списка MCP.");
  if (Array.isArray(servers) && servers.every((server) => Boolean(server) && typeof server === "object" && !Array.isArray(server))) return servers as ManagedMcp[];
  throw new Error(`Для узла «${target}» список MCP недоступен: Hub вернул ответ без перечня сервисов. Ответ не подтверждает, что список пуст.`);
}

export default function McpManageScreen() {
  const [hosts, setHosts] = useState<Server[]>([]);
  const [host, setHost] = useState("hub");
  const [rows, setRows] = useState<ManagedMcp[]>([]);
  const [listState, setListState] = useState<ListState>("idle");
  const [retention, setRetention] = useState(30);
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [result, setResult] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [listNotice, setListNotice] = useState<string | null>(null);
  const [mutationUnknown, setMutationUnknown] = useState(false);

  const loadBase = useCallback(async (signal?: AbortSignal) => {
    try {
      const [overviewResponse, settingsResponse] = await Promise.all([
        fetch("/admin/api/overview?limit=1", { credentials: "same-origin", signal, headers: { Accept: "application/json" } }),
        fetch("/admin/api/settings", { credentials: "same-origin", signal, headers: { Accept: "application/json" } }),
      ]);
      if (!overviewResponse.ok || !settingsResponse.ok) throw new Error(`Hub вернул HTTP ${!overviewResponse.ok ? overviewResponse.status : settingsResponse.status}`);
      const overview = await overviewResponse.json() as { servers?: Server[] };
      const settings = await settingsResponse.json() as { settings?: { stale_mcp_retention_days?: number } };
      const shellHosts = (overview.servers ?? []).filter((server) => String(server.server_id || "").startsWith("shell:"));
      setHosts([{ server_id: "hub", status: "local" }, ...shellHosts]);
      if (typeof settings.settings?.stale_mcp_retention_days === "number") setRetention(settings.settings.stale_mcp_retention_days);
      setError(null);
    } catch (caught) {
      if ((caught as DOMException).name !== "AbortError") setError(caught instanceof Error ? caught.message : "Не удалось загрузить MCP настройки");
    }
  }, []);

  useEffect(() => { const controller = new AbortController(); void loadBase(controller.signal); return () => controller.abort(); }, [loadBase]);

  const manage = async (payload: Record<string, unknown>, refreshList = false) => {
    const listing = payload.action === "list";
    if (listing) { setRows([]); setListState("loading"); setListNotice(null); }
    setBusy(true); setError(null);
    let data: unknown;
    try {
      data = await requestJson("/admin/api/mcp/manage", { method: "POST", body: JSON.stringify(payload) });
    } catch (caught) {
      if (listing) { setRows([]); setListState("unavailable"); }
      if (refreshList && (!(caught instanceof HttpResponseError) || caught.status >= 500)) {
        setMutationUnknown(true);
        setError(`Результат операции MCP неизвестен: запрос отправлен, но подтверждение не получено. Запрос не повторялся; проверьте состояние узла перед новой операцией.`);
      } else {
        setError(caught instanceof Error ? caught.message : "MCP операция не выполнена");
      }
      return null;
    }
    if (refreshList && data && typeof data === "object" && !Array.isArray(data) && "ok" in data && (data as { ok?: unknown }).ok === false) {
      setError("Hub отклонил операцию MCP; запрос не считается выполненным.");
      return null;
    }
    setResult(data);
    if (listing) {
      try {
        setRows(extractServers(data, String(payload.target ?? host)));
        setListState("loaded");
      } catch (caught) {
        setRows([]); setListState("unavailable");
        setError(caught instanceof Error ? caught.message : "Список MCP недоступен");
      }
    }
    if (payload.action === "status") {
      const ref = String(payload.ref ?? payload.name ?? "");
      const status = statusFromResponse(data);
      if (ref && status) setRows((current) => current.map((row) => (row.ref === ref || row.server_id === ref) ? { ...row, status } : row));
    }
    if (refreshList && payload.action !== "list") {
      try {
        const listed = await requestJson("/admin/api/mcp/manage", { method: "POST", body: JSON.stringify({ target: host, action: "list" }) });
        setRows(extractServers(listed, host));
        setListState("loaded");
        setListNotice(null);
      } catch {
        setRows([]); setListState("unavailable");
        const action = payload.action === "add" ? `Добавление «${String(payload.name ?? "MCP")}» выполнено.` : `Операция «${String(payload.action)}» выполнена.`;
        setListNotice(`${action} Список MCP недоступен: Hub не вернул перечень сервисов.`);
      }
    }
    return data;
  };

  const runManage = async (payload: Record<string, unknown>, refreshList = false) => {
    try { return await manage(payload, refreshList); }
    finally { setBusy(false); }
  };

  const saveRetention = async () => {
    setBusy(true); setError(null);
    try {
      const data = await requestJson("/admin/api/settings", { method: "PUT", body: JSON.stringify({ stale_mcp_retention_days: retention }) });
      setResult(data);
    } catch (caught) { setError(caught instanceof Error ? caught.message : "Настройка не сохранена"); }
    finally { setBusy(false); }
  };

  const add = async () => {
    let args: unknown;
    let env: unknown;
    try { args = JSON.parse(draft.args || "[]"); } catch (caught) { setError(`Args: ${caught instanceof Error ? caught.message : "некорректный JSON"}`); return; }
    try { env = JSON.parse(draft.env || "{}"); } catch (caught) { setError(`Env: ${caught instanceof Error ? caught.message : "некорректный JSON"}`); return; }
    if (!draft.name.trim()) { setError("Укажите имя MCP"); return; }
    if (!draft.url.trim() && !draft.command.trim()) { setError("Укажите удалённый адрес или команду запуска"); return; }
    const payload: Record<string, unknown> = { target: host, action: "add", name: draft.name.trim(), args, env, install: draft.install, force: draft.force, disabled: draft.disabled };
    if (draft.serverId.trim()) payload.server_id = draft.serverId.trim();
    if (draft.url.trim()) payload.url = draft.url.trim();
    if (draft.command.trim()) payload.command = draft.command.trim();
    if (draft.runAs.trim()) payload.run_as_user = draft.runAs.trim();
    if (draft.backend) payload.backend = draft.backend;
    if (draft.stdio) payload.stdio_format = draft.stdio;
    const done = mutationUnknown ? null : await runManage(payload, true);
    if (done) setDraft(emptyDraft);
  };

  const hostLabel = useMemo(() => hosts.find((item) => item.server_id === host), [hosts, host]);

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="РАСШИРЕННЫЙ РЕЖИМ" title="Сервисы MCP" description="Добавление и обслуживание системных сервисов. Обычно менять этот раздел не требуется." actions={<StatusBadge state={error ? "error" : "ready"}>{busy ? "Выполняем…" : hostLabel?.status || "Готово"}</StatusBadge>} />
    <ErrorNotice message={error} />
    {listNotice && <div className="state-panel card standalone-state compact-notice" role="status">{listNotice}</div>}
    <section className="card mcp-retention-card"><div><p className="section-kicker">ОЧИСТКА</p><h2>Автоматическая очистка старых MCP</h2><p className="muted-help">Серверные агенты не удаляются.</p></div><div className="inline-setting"><input type="number" min={1} max={3650} value={retention} onChange={(event) => setRetention(Number(event.target.value) || 30)} /><span>дней</span><button className="button secondary" type="button" disabled={busy} onClick={() => void saveRetention()}>Сохранить</button></div></section>
    <section className="mcp-native-layout">
      <article className="card mcp-list-card">
        <div className="card-heading"><div><p className="section-kicker">СЕРВИСЫ</p><h2>{host === "hub" ? "MCP в реестре Hub" : "Дочерние MCP на узле"}</h2></div><div className="mcp-list-actions"><select value={host} onChange={(event) => { setHost(event.target.value); setRows([]); setListState("idle"); setListNotice(null); }}>{hosts.map((item) => <option key={item.server_id} value={item.server_id}>{item.server_id} ({serverStatusLabel(item.status)})</option>)}</select><button className="button secondary" type="button" disabled={busy || mutationUnknown} onClick={() => void runManage({ target: host, action: "list" })}>{listState === "loading" ? "Загружаю…" : "Обновить список"}</button></div></div>
        <p className="muted-help">{host === "hub" ? "Показывает Hub core, virtual MCP definitions, зарегистрированные серверы и child MCP из последнего heartbeat. Для свежих конфигураций выберите ShellMCP узел. Парные control/file маршруты скрыты." : "Показывает конфигурации из supervisor выбранного ShellMCP узла. «Настроен» не означает, что лениво запускаемый процесс сейчас работает."}</p>
        {rows.length === 0 ? <div className="compact-empty"><strong>{listState === "loaded" ? "Список MCP пуст" : listState === "unavailable" ? "Список недоступен" : "Список не загружен"}</strong><span>{listState === "loaded" ? (host === "hub" ? "В реестре Hub нет MCP-серверов." : "Выбранный узел сообщил, что дочерних MCP-конфигураций нет.") : listState === "unavailable" ? "Источник не вернул подтверждённый список; пустым он не считается." : "Нажмите «Обновить список» для выбранного источника."}</span></div> : <div className="mcp-service-list">{rows.map((row) => <article className="mcp-service-row" key={row.server_id || row.ref || row.name}><div><strong>{row.name || "Без имени"}</strong><small>{row.server_id || row.command || row.url || "—"}</small><div className="pill-wrap"><span className="small-pill">{inventoryCategoryLabel(row.registry_category)}</span><span className={`small-pill ${row.enabled === false ? "is-disabled" : ""}`}>{inventoryStatusLabel(row)}</span>{row.transport && <span className="small-pill">{row.transport}</span>}</div></div>{host !== "hub" && <div className="row-actions"><button className="text-button" type="button" disabled={busy || mutationUnknown} onClick={() => void runManage({ target: host, action: "status", ref: row.ref || row.server_id })}>Обновить состояние</button></div>}</article>)}</div>}
      </article>
      {host !== "hub" && <article className="card mcp-add-card"><div className="card-heading"><div><p className="section-kicker">ДОБАВЛЕНИЕ</p><h2>Добавить MCP</h2></div></div><div className="mcp-form-grid">
        <label><span>Имя</span><input value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} placeholder="my-mcp" /></label>
        <label><span>ID сервера</span><input value={draft.serverId} onChange={(event) => setDraft({ ...draft, serverId: event.target.value })} placeholder="опционально" /></label>
        <label className="span2"><span>Удалённый адрес</span><input value={draft.url} onChange={(event) => setDraft({ ...draft, url: event.target.value })} placeholder="https://.../mcp" /></label>
        <label className="span2"><span>Команда запуска</span><input value={draft.command} onChange={(event) => setDraft({ ...draft, command: event.target.value })} placeholder="npx / python3" /></label>
        <label><span>Параметры запуска (JSON)</span><textarea rows={4} value={draft.args} onChange={(event) => setDraft({ ...draft, args: event.target.value })} /></label>
        <label><span>Переменные окружения (JSON)</span><textarea rows={4} value={draft.env} onChange={(event) => setDraft({ ...draft, env: event.target.value })} /></label>
        <label><span>Запускать от пользователя</span><input value={draft.runAs} onChange={(event) => setDraft({ ...draft, runAs: event.target.value })} /></label>
        <label><span>Система запуска</span><select value={draft.backend} onChange={(event) => setDraft({ ...draft, backend: event.target.value })}><option value="">auto</option><option value="systemd">systemd</option><option value="launchd">launchd</option><option value="windows-task">windows-task</option></select></label>
        <label><span>Формат обмена</span><select value={draft.stdio} onChange={(event) => setDraft({ ...draft, stdio: event.target.value })}><option value="">auto</option><option value="ndjson">ndjson</option><option value="jsonl">jsonl</option><option value="framed">framed</option><option value="content-length">content-length</option></select></label>
        <div className="mcp-checks"><label><input type="checkbox" checked={draft.install} onChange={(event) => setDraft({ ...draft, install: event.target.checked })} /> Установить сразу</label><label><input type="checkbox" checked={draft.force} onChange={(event) => setDraft({ ...draft, force: event.target.checked })} /> Принудительно</label><label><input type="checkbox" checked={draft.disabled} onChange={(event) => setDraft({ ...draft, disabled: event.target.checked })} /> Создать выключенным</label></div>
      </div><button className="button primary full-button" type="button" disabled={busy || mutationUnknown} onClick={() => void add()}>Добавить MCP</button></article>}
    </section>
    {result !== null && <details className="card operation-result"><summary>Технический ответ системы</summary><pre className="raw-native-box">{JSON.stringify(result, null, 2)}</pre></details>}
  </div>;
}
