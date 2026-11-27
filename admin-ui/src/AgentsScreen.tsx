import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { getLatestJobForServer, readServerResourceJob, sampleServerResources, type ResourceComponent, type ResourceSampleResult } from "./api";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";
import { formatServerLastSeen, formatTimestamp, friendlyServerName, hasServerLastSeen, serverRowQualifier, serverStatusLabel } from "./ui/format";

type Server = {
  server_id?: string;
  name?: string;
  status?: string;
  kind?: string;
  transport?: string;
  last_seen?: string | number;
  capabilities?: string[];
  meta?: Record<string, unknown>;
};

type Overview = { servers?: Server[] };

type ConnectInfo = {
  hub_url?: string;
  mcp_endpoint?: string;
  shell_token?: string;
  transport?: string;
  install_command?: string;
  env_block?: string;
};

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return <button className="button secondary" type="button" onClick={async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch { setCopied(false); }
  }}>{copied ? "Скопировано" : label}</button>;
}

function ConnectAgentPanel() {
  const [info, setInfo] = useState<ConnectInfo | null>(null);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const reveal = async () => {
    if (open) { setOpen(false); return; }
    try {
      setLoading(true);
      const response = await fetch("/admin/api/agents/connect-info", { credentials: "same-origin", headers: { Accept: "application/json" } });
      const data = await response.json() as ConnectInfo & { detail?: string };
      if (!response.ok) throw new Error(data.detail || `Hub вернул HTTP ${response.status}`);
      setInfo(data);
      setError(null);
      setOpen(true);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Не удалось получить код подключения");
    } finally { setLoading(false); }
  };

  return <section className="card connect-agent-panel">
    <div className="card-heading"><div><p className="section-kicker">ОНБОРДИНГ</p><h2>Подключить новый shell MCP</h2></div><button className="text-button" type="button" disabled={loading} onClick={() => void reveal()}>{loading ? "Получаем…" : open ? "Скрыть" : "Показать код подключения"}</button></div>
    {open && info && <>
      <p className="muted-help">Установи агента на новой машине и задай ему переменные окружения ниже. <code>{info.hub_url}</code> — канонический адрес этого хаба.</p>
      <div className="connect-actions"><pre className="connect-code">{info.install_command}</pre><CopyButton value={info.install_command ?? ""} label="Копировать установку" /></div>
      <div className="connect-actions"><pre className="connect-code">{info.env_block}</pre><CopyButton value={info.env_block ?? ""} label="Копировать переменные" /></div>
      <p className="muted-help">SHELL_TOKEN — общий агентный ключ этого хаба: он даёт право выполнять команды на машине, где он прописан. Не публикуй блок целиком и меняй <code>SHELL_NAME</code> на имя машины.</p>
    </>}
    {error && <ErrorNotice message={error} />}
  </section>;
}


function textValue(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return String(value);
  try { return JSON.stringify(value); } catch { return String(value); }
}

function waitForResourceJob(jobId: string, serverId: string, signal: AbortSignal): Promise<ResourceSampleResult> {
  return new Promise((resolve, reject) => {
    let attempts = 0;
    const poll = async () => {
      if (signal.aborted) {
        reject(new DOMException("Aborted", "AbortError"));
        return;
      }
      if (attempts >= 8) {
        resolve({ server_id: serverId, status: "pending", job_id: jobId });
        return;
      }
      attempts += 1;
      try {
        await new Promise<void>((done, fail) => {
          const timer = window.setTimeout(done, 400);
          signal.addEventListener("abort", () => { window.clearTimeout(timer); fail(new DOMException("Aborted", "AbortError")); }, { once: true });
        });
        const next = await readServerResourceJob(jobId, serverId, signal);
        if (next.status === "pending") void poll();
        else resolve(next);
      } catch (error) { reject(error); }
    };
    void poll();
  });
}

function componentSummary(component: ResourceComponent): string {
  if (component.status === "unavailable") return component.error ? `Недоступно: ${component.error}` : "Недоступно";
  return component.status === "partial" ? "Частичные данные" : "Данные получены";
}

function formatResourceBytes(value: number | undefined): string {
  if (value === undefined) return "Нет данных";
  return `${new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 1 }).format(value / (1024 ** 3))} ГиБ`;
}

function resourceStateLabel(state: ResourceSampleResult["status"]): string {
  if (state === "available") return "Измерено";
  if (state === "partial") return "Измерено частично";
  if (state === "pending") return "Измерение выполняется";
  return "Метрики недоступны";
}

function resourceComponentValue(component: ResourceComponent, value: string): string {
  return component.status === "unavailable" ? componentSummary(component) : value;
}

export default function AgentsScreen() {
  const [servers, setServers] = useState<Server[]>([]);
  const [filter, setFilter] = useState("");
  const [status, setStatus] = useState("all");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [resources, setResources] = useState<ResourceSampleResult | null>(null);
  const [resourcesLoading, setResourcesLoading] = useState(false);
  const [resourcesError, setResourcesError] = useState<string | null>(null);
  const [latestJob, setLatestJob] = useState<Awaited<ReturnType<typeof getLatestJobForServer>>>(null);
  const [latestJobLoading, setLatestJobLoading] = useState(false);
  const [latestJobError, setLatestJobError] = useState<string | null>(null);
  const resourceRequest = useRef<AbortController | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      setLoading(true);
      const response = await fetch("/admin/api/overview?limit=20", { credentials: "same-origin", signal, headers: { Accept: "application/json" } });
      if (!response.ok) throw new Error(`Hub вернул HTTP ${response.status}`);
      const data = await response.json() as Overview;
      setServers(Array.isArray(data.servers) ? data.servers : []);
      setError(null);
    } catch (caught) {
      if ((caught as DOMException).name !== "AbortError") setError(caught instanceof Error ? caught.message : "Не удалось загрузить серверы");
    } finally { if (!signal?.aborted) setLoading(false); }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    const timer = window.setInterval(() => void load(controller.signal), 15_000);
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [load]);

  const filtered = useMemo(() => servers.filter((server) => {
    if (status === "offline" && server.status !== "offline" && server.status !== "stale") return false;
    if (status !== "all" && status !== "offline" && server.status !== status) return false;
    if (!filter.trim()) return true;
    const displayName = friendlyServerName(server.server_id, server.name);
    const qualifier = serverRowQualifier(server.server_id, server.name, server.kind, server.meta);
    return `${displayName} ${qualifier} ${JSON.stringify(server)}`.toLowerCase().includes(filter.trim().toLowerCase());
  }), [servers, filter, status]);
  const selected = servers.find((server) => server.server_id === selectedId) ?? null;
  const selectedShellId = selected?.server_id?.startsWith("shell:") ? selected.server_id : null;

  const loadResources = useCallback(async (serverId: string, resumeJobId?: string) => {
    resourceRequest.current?.abort();
    const controller = new AbortController();
    resourceRequest.current = controller;
    setResources(null);
    setResourcesError(null);
    setResourcesLoading(true);
    try {
      let result: ResourceSampleResult;
      if (resumeJobId) {
        result = await waitForResourceJob(resumeJobId, serverId, controller.signal);
      } else {
        result = await sampleServerResources(serverId, controller.signal);
        if (result.status === "pending") result = await waitForResourceJob(result.job_id, serverId, controller.signal);
      }
      if (!controller.signal.aborted) setResources(result);
    } catch (caught) {
      if (!controller.signal.aborted && (caught as DOMException).name !== "AbortError") {
        setResourcesError(caught instanceof Error ? caught.message : "Не удалось получить метрики узла.");
      }
    } finally {
      if (!controller.signal.aborted) setResourcesLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!selectedShellId) {
      resourceRequest.current?.abort();
      setResources(null);
      setResourcesError(null);
      setResourcesLoading(false);
      return;
    }
    void loadResources(selectedShellId);
    return () => resourceRequest.current?.abort();
  }, [selectedShellId, loadResources]);

  useEffect(() => {
    if (!selectedShellId || (selected?.status !== "offline" && selected?.status !== "stale")) {
      setLatestJob(null);
      setLatestJobError(null);
      return;
    }
    const controller = new AbortController();
    setLatestJob(null);
    setLatestJobError(null);
    setLatestJobLoading(true);
    void getLatestJobForServer(selectedShellId, controller.signal).then((job) => {
      if (!controller.signal.aborted) setLatestJob(job);
    }).catch((caught) => {
      if (!controller.signal.aborted) setLatestJobError(caught instanceof Error ? caught.message : "Не удалось загрузить историю задач.");
    }).finally(() => { if (!controller.signal.aborted) setLatestJobLoading(false); });
    return () => controller.abort();
  }, [selectedShellId, selected?.status]);

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="СЕРВЕРЫ" title="Серверы" description="Какие серверы работают, какие недоступны и когда они отвечали в последний раз." actions={<><StatusBadge state={error ? "error" : "ready"}>{loading ? "Обновляем…" : `${filtered.length} узлов`}</StatusBadge><button className="button secondary" type="button" onClick={() => void load()} disabled={loading}>Обновить</button></>} />
    <ErrorNotice message={error} />
    <ConnectAgentPanel />
    <section className={`agent-native-layout ${selected ? "has-detail" : ""}`}>
      <article className="card agent-native-list">
        <div className="list-toolbar"><input className="search" value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="Фильтр серверов" /><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="all">Все статусы</option><option value="online">Работают</option><option value="offline">Недоступны</option><option value="stale">Давно не отвечают</option><option value="failed">Ошибка</option></select></div>
        {filtered.length === 0 && !loading ? <div className="compact-empty"><strong>Серверов не найдено</strong><span>Измените фильтр или обновите данные.</span></div> : <div className="compact-list">{filtered.map((server) => {
          const qualifier = serverRowQualifier(server.server_id, server.name, server.kind, server.meta);
          const hasLastSeen = hasServerLastSeen(server.last_seen);
          const lastSeen = formatServerLastSeen(server.status, server.last_seen);
          return <button type="button" className={`node-native-row ${selectedId === server.server_id ? "selected" : ""}`} key={server.server_id || server.name} onClick={() => setSelectedId(server.server_id || null)}><span className={`status-dot status-${server.status || "unknown"}`} /><span className="node-native-main"><strong>{friendlyServerName(server.server_id, server.name)}{qualifier ? <span className="small-pill"> · {qualifier}</span> : null}</strong><small>{hasLastSeen ? <span title={formatTimestamp(server.last_seen)}>Последний ответ: {lastSeen}</span> : "Ещё не отвечал"}</small></span><span className="node-native-status">{serverStatusLabel(server.status)}</span></button>;
        })}</div>}
      </article>
      {selected && <aside className="card agent-detail-pane">
        <div className="card-heading"><div><p className="section-kicker">ПОДРОБНОСТИ</p><h2>{friendlyServerName(selected.server_id, selected.name)}</h2></div><button className="text-button" type="button" onClick={() => setSelectedId(null)}>Закрыть</button></div>
        <dl className="detail-facts"><div><dt>Технический ID</dt><dd>{selected.server_id || "—"}</dd></div><div><dt>Состояние</dt><dd>{serverStatusLabel(selected.status)}</dd></div><div><dt>Тип узла</dt><dd>{serverRowQualifier(selected.server_id, selected.name, selected.kind, selected.meta) || selected.kind || "—"}</dd></div><div><dt>Тип подключения</dt><dd>{selected.kind || "—"}</dd></div><div><dt>Последний ответ</dt><dd title={hasServerLastSeen(selected.last_seen) ? formatTimestamp(selected.last_seen) : undefined}>{formatServerLastSeen(selected.status, selected.last_seen)}</dd></div></dl>
        {selectedShellId && <section className="detail-section" aria-label="Метрики узла"><div className="card-heading"><div><h3>Ресурсы узла</h3><p className="muted-help">Один снимок для точного узла {selectedShellId}.</p></div><button className="button secondary" type="button" disabled={resourcesLoading} onClick={() => void loadResources(selectedShellId, resources?.status === "pending" ? resources.job_id : undefined)}>{resourcesLoading ? "Получаем…" : resources?.status === "pending" ? "Проверить результат" : "Обновить метрики"}</button></div>
          {resourcesLoading && <p role="status">Запрашиваем актуальные метрики…</p>}
          {resourcesError && <p className="warning-text" role="alert">{resourcesError}</p>}
          {resources && resources.status !== "pending" && <><p className="muted-help" role="status">{resourceStateLabel(resources.status)} · измерено: {resources.sampled_at ? formatTimestamp(resources.sampled_at) : "снимок не выполнялся"}{resources.node_status ? ` · состояние узла: ${serverStatusLabel(resources.node_status)}` : ""}{resources.error ? ` · ${resources.error}` : ""}</p><dl className="detail-facts resource-facts"><div><dt>CPU</dt><dd>{resourceComponentValue(resources.cpu, resources.cpu.utilization_percent === undefined ? componentSummary(resources.cpu) : `${resources.cpu.utilization_percent.toLocaleString("ru-RU")} % · ${resources.cpu.cores ?? "?"} ядер`)}{resources.cpu.status !== "unavailable" && resources.cpu.sample_interval_ms !== undefined ? ` · замер ${resources.cpu.sample_interval_ms} мс` : ""}</dd></div><div><dt>Память</dt><dd>{resourceComponentValue(resources.memory, resources.memory.used_bytes === undefined ? componentSummary(resources.memory) : `${formatResourceBytes(resources.memory.used_bytes)} из ${formatResourceBytes(resources.memory.total_bytes)} · свободно ${formatResourceBytes(resources.memory.available_bytes)}`)}</dd></div><div><dt>Корневой диск</dt><dd>{resourceComponentValue(resources.root_filesystem, resources.root_filesystem.used_bytes === undefined ? componentSummary(resources.root_filesystem) : `${resources.root_filesystem.mount || "/"}: занято ${formatResourceBytes(resources.root_filesystem.used_bytes)} из ${formatResourceBytes(resources.root_filesystem.total_bytes)} · свободно ${formatResourceBytes(resources.root_filesystem.free_bytes)}`)}</dd></div></dl></>}
          {resources?.status === "pending" && <p role="status">Замер выполняется. Результат будет прочитан по уже созданной задаче без повторного запуска.</p>}
        </section>}
        {selectedShellId && (selected.status === "offline" || selected.status === "stale") && <section className="detail-section" aria-label="Последняя задача узла"><h3>Последняя задача узла</h3>{latestJobLoading && <p role="status">Ищем задачу в сохранённой истории…</p>}{latestJobError && <p className="warning-text" role="alert">{latestJobError}</p>}{!latestJobLoading && !latestJobError && !latestJob && <p className="muted-help">Для этого узла сохранённых задач нет.</p>}{latestJob && <p><a className="button secondary" href={`#jobs?server_id=${encodeURIComponent(latestJob.server_id)}&job_id=${encodeURIComponent(latestJob.job_id)}`}>Открыть последнюю задачу: {latestJob.tool_name || latestJob.job_id}</a><small className="muted-help">История загружена отдельно от недоступного узла.</small></p>}</section>}
        <details className="detail-section technical-details"><summary>Технические возможности</summary><div className="pill-wrap">{selected.capabilities?.length ? selected.capabilities.map((cap) => <span className="small-pill" key={cap}>{cap}</span>) : <span className="muted-help">Не переданы</span>}</div></details>
        <details className="detail-section technical-details"><summary>Метаданные</summary>{Object.keys(selected.meta ?? {}).length ? <dl className="meta-grid">{Object.entries(selected.meta ?? {}).map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{textValue(value)}</dd></div>)}</dl> : <p className="muted-help">Нет метаданных.</p>}</details>
      </aside>}
    </section>
  </div>;
}
