import { useCallback, useEffect, useMemo, useState } from "react";
import { serverStatusLabel } from "./ui/format";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";

type Server = { server_id?: string; status?: string; meta?: Record<string, unknown> };
type ManagedMcp = { name?: string; server_id?: string; command?: string; url?: string; args?: unknown[]; env?: Record<string, unknown>; enabled?: boolean; stdio_format?: string };

type ManageResponse = { response?: { servers?: ManagedMcp[] }; servers?: ManagedMcp[] };

type Draft = { name: string; serverId: string; url: string; command: string; args: string; env: string; runAs: string; backend: string; stdio: string; install: boolean; force: boolean; disabled: boolean };
const emptyDraft: Draft = { name: "", serverId: "", url: "", command: "", args: "[]", env: "{}", runAs: "", backend: "", stdio: "", install: true, force: false, disabled: false };

async function requestJson(path: string, init?: RequestInit): Promise<unknown> {
  const response = await fetch(path, { credentials: "same-origin", ...init, headers: { Accept: "application/json", "Content-Type": "application/json", ...(init?.headers ?? {}) } });
  const text = await response.text();
  let data: unknown = text;
  try { data = text ? JSON.parse(text) : {}; } catch { /* keep text */ }
  if (!response.ok) throw new Error(typeof data === "object" && data && "detail" in data ? String((data as { detail?: unknown }).detail) : `HTTP ${response.status}`);
  return data;
}

function extractServers(value: unknown): ManagedMcp[] {
  const data = value as ManageResponse;
  return data?.response?.servers ?? data?.servers ?? [];
}

export default function McpManageScreen() {
  const [hosts, setHosts] = useState<Server[]>([]);
  const [host, setHost] = useState("hub");
  const [rows, setRows] = useState<ManagedMcp[]>([]);
  const [retention, setRetention] = useState(30);
  const [keepService, setKeepService] = useState(false);
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [result, setResult] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadBase = useCallback(async (signal?: AbortSignal) => {
    try {
      const [overviewResponse, settingsResponse] = await Promise.all([
        fetch("/admin/api/overview?limit=1", { credentials: "same-origin", signal, headers: { Accept: "application/json" } }),
        fetch("/admin/api/settings", { credentials: "same-origin", signal, headers: { Accept: "application/json" } }),
      ]);
      if (!overviewResponse.ok || !settingsResponse.ok) throw new Error(`Hub вернул HTTP ${!overviewResponse.ok ? overviewResponse.status : settingsResponse.status}`);
      const overview = await overviewResponse.json() as { servers?: Server[] };
      const settings = await settingsResponse.json() as { settings?: { stale_mcp_retention_days?: number } };
      const shellHosts = (overview.servers ?? []).filter((server) => String(server.server_id || "").startsWith("shell:") || server.meta?.transport_layer === "mcp_tunnel");
      setHosts([{ server_id: "hub", status: "local" }, ...shellHosts]);
      if (typeof settings.settings?.stale_mcp_retention_days === "number") setRetention(settings.settings.stale_mcp_retention_days);
      setError(null);
    } catch (caught) {
      if ((caught as DOMException).name !== "AbortError") setError(caught instanceof Error ? caught.message : "Не удалось загрузить MCP настройки");
    }
  }, []);

  useEffect(() => { const controller = new AbortController(); void loadBase(controller.signal); return () => controller.abort(); }, [loadBase]);

  const manage = async (payload: Record<string, unknown>, refreshList = false) => {
    setBusy(true); setError(null);
    try {
      const data = await requestJson("/admin/api/mcp/manage", { method: "POST", body: JSON.stringify(payload) });
      setResult(data);
      const next = extractServers(data);
      if (next.length || payload.action === "list") setRows(next);
      if (refreshList && payload.action !== "list") {
        const listed = await requestJson("/admin/api/mcp/manage", { method: "POST", body: JSON.stringify({ target: host, action: "list" }) });
        setRows(extractServers(listed));
      }
      return data;
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "MCP операция не выполнена");
      return null;
    } finally { setBusy(false); }
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
    const done = await manage(payload, true);
    if (done) setDraft(emptyDraft);
  };

  const hostLabel = useMemo(() => hosts.find((item) => item.server_id === host), [hosts, host]);

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="РАСШИРЕННЫЙ РЕЖИМ" title="Сервисы MCP" description="Добавление и обслуживание системных сервисов. Обычно менять этот раздел не требуется." actions={<StatusBadge state={error ? "error" : "ready"}>{busy ? "Выполняем…" : hostLabel?.status || "Готово"}</StatusBadge>} />
    <ErrorNotice message={error} />
    <section className="card mcp-retention-card"><div><p className="section-kicker">ОЧИСТКА</p><h2>Автоматическая очистка старых MCP</h2><p className="muted-help">Серверные агенты не удаляются.</p></div><div className="inline-setting"><input type="number" min={1} max={3650} value={retention} onChange={(event) => setRetention(Number(event.target.value) || 30)} /><span>дней</span><button className="button secondary" type="button" disabled={busy} onClick={() => void saveRetention()}>Сохранить</button></div></section>
    <section className="mcp-native-layout">
      <article className="card mcp-list-card">
        <div className="card-heading"><div><p className="section-kicker">СЕРВИСЫ</p><h2>Установленные MCP</h2></div><div className="mcp-list-actions"><select value={host} onChange={(event) => { setHost(event.target.value); setRows([]); }}>{hosts.map((item) => <option key={item.server_id} value={item.server_id}>{item.server_id} ({serverStatusLabel(item.status)})</option>)}</select><button className="button secondary" type="button" disabled={busy} onClick={() => void manage({ target: host, action: "list" })}>Список</button><button className="button secondary" type="button" disabled={busy} onClick={() => void manage({ target: host, action: "status", ...(draft.backend ? { backend: draft.backend } : {}) })}>Статус</button></div></div>
        <label className="keep-service-line"><input type="checkbox" checked={keepService} onChange={(event) => setKeepService(event.target.checked)} /> При удалении сохранить описание системного сервиса</label>
        {rows.length === 0 ? <div className="compact-empty"><strong>MCP не загружены</strong><span>Нажмите «Список» для выбранного узла.</span></div> : <div className="mcp-service-list">{rows.map((row) => <article className="mcp-service-row" key={row.name || row.server_id}><div><strong>{row.name || "Без имени"}</strong><small>{row.server_id || row.command || row.url || "—"}</small><div className="pill-wrap"><span className={`small-pill ${row.enabled === false ? "is-disabled" : ""}`}>{row.enabled === false ? "Выключен" : "Работает"}</span>{row.stdio_format && <span className="small-pill">{row.stdio_format}</span>}</div></div><div className="row-actions"><button className="text-button" type="button" disabled={busy} onClick={() => void manage({ target: host, action: "status", name: row.name, ...(draft.backend ? { backend: draft.backend } : {}) })}>Состояние</button><button className="text-button" type="button" disabled={busy} onClick={() => void manage({ target: host, action: "install", name: row.name, ...(draft.backend ? { backend: draft.backend } : {}) }, true)}>Установить</button><button className="text-button danger-text" type="button" disabled={busy} onClick={() => { if (window.confirm(`Удалить MCP ${row.name} на ${host}?`)) void manage({ target: host, action: "remove", name: row.name, keep_service: keepService, ...(draft.backend ? { backend: draft.backend } : {}) }, true); }}>Удалить</button></div></article>)}</div>}
      </article>
      <article className="card mcp-add-card"><div className="card-heading"><div><p className="section-kicker">ДОБАВЛЕНИЕ</p><h2>Добавить MCP</h2></div></div><div className="mcp-form-grid">
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
      </div><button className="button primary full-button" type="button" disabled={busy} onClick={() => void add()}>Добавить MCP</button></article>
    </section>
    {result !== null && <details className="card operation-result"><summary>Технический ответ системы</summary><pre className="raw-native-box">{JSON.stringify(result, null, 2)}</pre></details>}
  </div>;
}
