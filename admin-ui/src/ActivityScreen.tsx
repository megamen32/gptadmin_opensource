import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";
import { getTaskReceipt } from "./api";
import "./ActivityScreen.css";

type AuditEntry = { time: string; name: string; fields?: Record<string, unknown> };
type JobEntry = {
  job_id?: string; server_id?: string; status?: string; tool_name?: string;
  created_at?: number | string; started_at?: number | string; completed_at?: number | string;
};
type AuditPage = { events?: AuditEntry[]; total?: number; clients?: Record<string, number> };
type JobPage = { recent?: JobEntry[] };
type Outcome = "ok" | "error" | "running";
type Operation = {
  id: string; event: AuditEntry; client: string; tool: string; target: string;
  time: number; end: number; outcome: Outcome; jobId: string; job?: JobEntry;
};
type Positioned = { item: Operation; x: number; width: number; track: number };
type Lane = { client: string; items: Positioned[]; tracks: number };
type CompactGroup = { key: string; x: number; outcome: Outcome; items: Operation[] };
function compactGroups(lane: Lane): CompactGroup[] {
  const groups = new Map<string, CompactGroup>();
  for (const { item, x } of lane.items) {
    const bucket = Math.floor(x / 28);
    const key = item.outcome + ":" + bucket;
    const group = groups.get(key) ?? { key, x: bucket * 28, outcome: item.outcome, items: [] };
    group.items.push(item);
    groups.set(key, group);
  }
  return [...groups.values()];
}
type Period = 15 | 60 | 360 | 1440;

const storageKey = "gptadmin:activity:hidden-clients:v1";
const periods: { value: Period; label: string }[] = [
  { value: 15, label: "15 минут" }, { value: 60, label: "1 час" },
  { value: 360, label: "6 часов" }, { value: 1440, label: "24 часа" },
];

function textValue(value: unknown): string {
  return value === undefined || value === null || value === "" ? "" : String(value);
}
function field(event: AuditEntry, key: string): string {
  return textValue(event.fields?.[key]);
}
function clientOf(event: AuditEntry): string {
  return field(event, "client_id") || field(event, "actor") || field(event, "subject") || "unattributed";
}
function labelClient(client: string): string {
  return client === "unattributed" ? "Без установленного клиента" : client;
}
function timestamp(value: unknown): number {
  if (typeof value === "number") return value < 1e12 ? value * 1000 : value;
  if (typeof value === "string" && /^\d+(\.\d+)?$/.test(value)) {
    const numeric = Number(value);
    return numeric < 1e12 ? numeric * 1000 : numeric;
  }
  return typeof value === "string" ? Date.parse(value) : NaN;
}
function timeLabel(value: number): string {
  return Number.isFinite(value) ? new Intl.DateTimeFormat("ru-RU", { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(value) : "—";
}
function dateLabel(value: number): string {
  return Number.isFinite(value) ? new Intl.DateTimeFormat("ru-RU", { dateStyle: "medium", timeStyle: "medium" }).format(value) : "—";
}
function durationLabel(ms: number): string {
  if (ms < 1000) return String(Math.max(0, Math.round(ms))) + " мс";
  if (ms < 60000) return (ms / 1000).toFixed(1) + " с";
  return (ms / 60000).toFixed(1) + " мин";
}
function isJobId(value: string): boolean {
  return value !== "" && value !== "none" && value !== "inline";
}
function statusOf(event: AuditEntry, job?: JobEntry): Outcome {
  if (job?.status === "running" || job?.status === "dispatching" || job?.status?.startsWith("queued")) return "running";
  if (job?.status === "failed" || job?.status === "error") return "error";
  if (field(event, "policy_decision") === "deny") return "error";
  const status = Number(field(event, "status"));
  return status >= 400 ? "error" : "ok";
}
function makeOperations(events: AuditEntry[], jobs: JobEntry[], now: number): Operation[] {
  const byId = new Map(jobs.filter((job) => job.job_id).map((job) => [job.job_id as string, job]));
  return events.flatMap((event, index) => {
    const time = timestamp(event.time);
    if (!Number.isFinite(time)) return [];
    const jobId = field(event, "result_reference");
    const job = isJobId(jobId) ? byId.get(jobId) : undefined;
    const start = timestamp(job?.started_at ?? job?.created_at);
    const complete = timestamp(job?.completed_at);
    const end = job && Number.isFinite(complete) && complete >= start
      ? complete : job && (job.status === "running" || job.status === "dispatching") ? now : time;
    const opTime = Number.isFinite(start) && start > 0 ? start : time;
    return [{
      id: [event.time, event.name, clientOf(event), field(event, "tool"), jobId, index].join("|"),
      event, client: clientOf(event), tool: field(event, "tool") || event.name,
      target: field(event, "target") || field(event, "server_id") || "—",
      time: opTime, end: Math.max(opTime, end), outcome: statusOf(event, job), jobId, job,
    }];
  });
}
function layoutLanes(items: Operation[], width: number, begin: number, finish: number): Lane[] {
  const groups = new Map<string, Operation[]>();
  for (const item of items) {
    const group = groups.get(item.client) ?? [];
    group.push(item);
    groups.set(item.client, group);
  }
  return [...groups].sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0]))
    .map(([client, entries]) => {
      const tracks: number[] = [];
      const placed: Positioned[] = [];
      for (const item of [...entries].sort((a, b) => a.time - b.time)) {
        const x = Math.max(0, Math.min(width - 14, (item.time - begin) / (finish - begin) * width));
        const trueEnd = Math.max(item.time, Math.min(item.end, finish));
        const span = Math.max(14, Math.min(width - x, (trueEnd - Math.max(item.time, begin)) / (finish - begin) * width));
        const track = tracks.findIndex((last) => last + 6 <= x);
        const i = track < 0 ? tracks.length : track;
        tracks[i] = x + span;
        placed.push({ item, x, width: span, track: i });
      }
      return { client, items: placed, tracks: Math.max(1, tracks.length) };
    });
}
function readHidden(): string[] {
  try {
    const value = JSON.parse(localStorage.getItem(storageKey) || "[]") as unknown;
    return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
  } catch { return []; }
}
function displayValue(value: unknown): string {
  if (typeof value === "object" && value !== null) return JSON.stringify(value, null, 2);
  return textValue(value) || "—";
}

export default function ActivityScreen({ mode = "timeline" }: { mode?: "timeline" | "requests" }) {
  const [period, setPeriod] = useState<Period>(60);
  const [scope, setScope] = useState<"calls" | "all">("calls");
  const [hidden, setHidden] = useState<string[]>(readHidden);
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [outcome, setOutcome] = useState("all");
  const [layout, setLayout] = useState<"timeline" | "list">(mode === "requests" ? "list" : "timeline");
  const [parallel, setParallel] = useState(false);
  const [cluster, setCluster] = useState<Operation[]>([]);
  const scrollRef = useRef<HTMLDivElement>(null);
  const [scrollLeft, setScrollLeft] = useState(0);
  const [scrollMax, setScrollMax] = useState(0);
  const [live, setLive] = useState(true);
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [jobs, setJobs] = useState<JobEntry[]>([]);
  const [clients, setClients] = useState<Record<string, number>>({});
  const [total, setTotal] = useState(0);
  const [now, setNow] = useState(Date.now());
  const [selected, setSelected] = useState<Operation | null>(null);
  const [jobDetail, setJobDetail] = useState<unknown>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [loading, setLoading] = useState(true);
  const [moreLoading, setMoreLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const requestGeneration = useRef(0);
  const detailGeneration = useRef(0);
  const origin = useRef(Date.now());

  useEffect(() => { localStorage.setItem(storageKey, JSON.stringify(hidden)); }, [hidden]);

  const parameters = useCallback((offset: number) => {
    const params = new URLSearchParams({ limit: "500", offset: String(offset), recent: "1", since: String(Math.floor((origin.current - period * 60000) / 1000)) });
    if (scope === "calls") params.set("name", "tool_policy_decision");
    if (query) params.set("q", query);
    for (const name of hidden) params.append("exclude_client", name);
    return params;
  }, [period, scope, query, hidden]);

  const load = useCallback(async (signal?: AbortSignal) => {
    const generation = ++requestGeneration.current;
    origin.current = Date.now();
    setLoading(true);
    setError(null);
    try {
      const [response, jobResponse] = await Promise.all([
        fetch("/admin/api/audit?" + parameters(0), { credentials: "same-origin", signal }),
        fetch("/admin/api/jobs?limit=300", { credentials: "same-origin", signal }).catch(() => null),
      ]);
      if (!response.ok) throw new Error("Hub вернул HTTP " + response.status);
      const result = await response.json() as AuditPage;
      let recentJobs: JobEntry[] = [];
      if (jobResponse?.ok) {
        const data = await jobResponse.json() as JobPage;
        recentJobs = Array.isArray(data.recent) ? data.recent : [];
      }
      if (signal?.aborted || requestGeneration.current !== generation) return;
      setEntries(Array.isArray(result.events) ? result.events : []);
      setClients(result.clients && typeof result.clients === "object" ? result.clients : {});
      setTotal(typeof result.total === "number" ? result.total : 0);
      setJobs(recentJobs);
      setNow(origin.current);
    } catch (caught) {
      if (!signal?.aborted && requestGeneration.current === generation && (caught as Error).name !== "AbortError") {
        setError(caught instanceof Error ? caught.message : "Не удалось загрузить операции");
      }
    } finally {
      if (!signal?.aborted && requestGeneration.current === generation) setLoading(false);
    }
  }, [parameters]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    if (live) {
      const timer = window.setInterval(() => void load(controller.signal), 15000);
      return () => { clearInterval(timer); controller.abort(); requestGeneration.current++; };
    }
    return () => { controller.abort(); requestGeneration.current++; };
  }, [load, live]);

  const loadMore = async () => {
    setLive(false);
    setMoreLoading(true); setError(null);
    const generation = requestGeneration.current;
    try {
      const response = await fetch("/admin/api/audit?" + parameters(entries.length), { credentials: "same-origin" });
      if (!response.ok) throw new Error("Hub вернул HTTP " + response.status);
      const data = await response.json() as AuditPage;
      if (generation === requestGeneration.current) setEntries((previous) => [...previous, ...(data.events ?? [])]);
    } catch (caught) {
      if (generation === requestGeneration.current) setError(caught instanceof Error ? caught.message : "Не удалось загрузить историю");
    } finally { setMoreLoading(false); }
  };

  const operations = useMemo(() => makeOperations(entries, jobs, now).filter((op) =>
    outcome === "all" || (outcome === "error" ? op.outcome === "error" : op.outcome !== "error")
  ), [entries, jobs, now, outcome]);
  const width = Math.max(1200, Math.min(7200, period * 24));
  const start = now - period * 60000;
  const lanes = useMemo(() => layoutLanes(operations, width, start, now), [operations, width, start, now]);
  useEffect(() => {
    const box = scrollRef.current;
    if (!box) return;
    const update = () => {
      setScrollMax(Math.max(0, box.scrollWidth - box.clientWidth));
      setScrollLeft(box.scrollLeft);
    };
    update();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
    observer?.observe(box);
    window.addEventListener("resize", update);
    return () => { observer?.disconnect(); window.removeEventListener("resize", update); };
  }, [width, layout, selected, operations.length]);
  const moveTimeline = (value: number) => {
    const box = scrollRef.current;
    if (box) { box.scrollLeft = Math.max(0, Math.min(scrollMax, value)); setScrollLeft(box.scrollLeft); }
  };
  useEffect(() => { setCluster([]); }, [period, scope, query, outcome, hidden]);
  const clientChoices = useMemo(() =>
    Object.entries(clients).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])),
  [clients]);
  const enabledClients = clientChoices.filter(([name]) => !hidden.includes(name)).length;
  const errorCount = operations.filter((op) => op.outcome === "error").length;
  const related = useMemo(() => {
    if (!selected) return [];
    const correlation = field(selected.event, "correlation_id") || field(selected.event, "trace_id");
    return operations.filter((op) => op.id !== selected.id && (
      (correlation !== "" && (field(op.event, "correlation_id") === correlation || field(op.event, "trace_id") === correlation)) ||
      (isJobId(selected.jobId) && op.jobId === selected.jobId)
    ));
  }, [operations, selected]);

  const select = useCallback(async (op: Operation) => {
    const generation = ++detailGeneration.current;
    setSelected(op);
    setJobDetail(null); setDetailError(null);
    if (!isJobId(op.jobId)) { setDetailLoading(false); return; }
    setDetailLoading(true);
    try {
      const data = await getTaskReceipt(op.jobId);
      if (detailGeneration.current === generation) setJobDetail(data);
    } catch (caught) {
      if (detailGeneration.current === generation) setDetailError(caught instanceof Error ? caught.message : "Данные задачи недоступны");
    } finally { if (detailGeneration.current === generation) setDetailLoading(false); }
  }, []);

  return <div className="page-shell activity-page">
    <PageHeader eyebrow="АКТИВНОСТЬ" title={mode === "requests" ? "Журнал запросов" : "Таймлайн операций"}
      description={mode === "requests" ? "Запросы к инструментам Hub: источник, действие, результат и связанные задачи." : "Кто обращается к Hub, какие инструменты вызывает и что происходит. Задачи показывают реальные интервалы, остальные события — момент вызова."}
      actions={<><StatusBadge state={error ? "error" : "ready"}>{loading ? "Синхронизация…" : live ? "Авто · 15 с" : "Авто выключено"}</StatusBadge><button className="button secondary" type="button" onClick={() => void load()} disabled={loading}>Обновить</button></>} />
    <div className="activity-stats" aria-label="Сводка">
      <div><span>Операций по фильтру</span><strong>{total.toLocaleString("ru-RU")}</strong></div>
      <div><span>Показано</span><strong>{operations.length.toLocaleString("ru-RU")}</strong></div>
      <div><span>Ошибок в выборке</span><strong>{errorCount}</strong></div>
      <div><span>Клиентов выбрано</span><strong>{enabledClients} / {clientChoices.length}</strong></div>
    </div>
    <div className="activity-controls card">
      <div className="activity-control-line">
        <label>Период <select value={period} onChange={(e) => setPeriod(Number(e.target.value) as Period)}>{periods.map((entry) => <option key={entry.value} value={entry.value}>{entry.label}</option>)}</select></label>
        <label>Тип <select value={scope} onChange={(e) => setScope(e.target.value as "calls" | "all")}><option value="calls">Вызовы инструментов</option><option value="all">Все события журнала</option></select></label>
        <label>Результат <select value={outcome} onChange={(e) => setOutcome(e.target.value)}><option value="all">Любой</option><option value="ok">Успешные и активные</option><option value="error">Ошибки и отказы</option></select></label>
        <label className="activity-live"><input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} /> Автообновление</label>
      </div>
      <form className="activity-search" onSubmit={(e) => { e.preventDefault(); setQuery(search.trim()); }}>
        <input aria-label="Поиск по операциям" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Инструмент, сервер, trace ID, клиент…" />
        <button className="button primary" type="submit">Найти</button>
        {(query || search) && <button className="button secondary" type="button" onClick={() => { setQuery(""); setSearch(""); }}>Сбросить</button>}
      </form>
      <details className="activity-client-selector" open>
        <summary><strong>Клиенты</strong><span>{enabledClients} из {clientChoices.length} · скрытые не учитываются сервером</span></summary>
        <div className="activity-client-actions">
          <button type="button" onClick={() => setHidden([])}>Выбрать всех</button>
          <button type="button" onClick={() => setHidden(clientChoices.map(([name]) => name))}>Снять все</button>
        </div>
        {clientChoices.length ? <div className="activity-client-grid">{clientChoices.map(([client, count]) =>
          <label key={client} className="activity-client-choice">
            <input type="checkbox" checked={!hidden.includes(client)} onChange={(e) =>
              setHidden((current) => e.target.checked ? current.filter((name) => name !== client) : [...current, client])
            } />
            <span title={client}>{labelClient(client)}</span><small>{count}</small>
          </label>
        )}</div> : <p className="muted-help">За выбранный период клиентов пока нет.</p>}
      </details>
    </div>
    <ErrorNotice message={error} />
    <div className="activity-view-header">
      <div className="activity-segment" role="group" aria-label="Вид операций">
        <button type="button" className={layout === "timeline" && !parallel ? "chosen" : ""} aria-pressed={layout === "timeline" && !parallel} onClick={() => { setLayout("timeline"); setParallel(false); }}>Компактно</button>
        <button type="button" className={layout === "timeline" && parallel ? "chosen" : ""} aria-pressed={layout === "timeline" && parallel} onClick={() => { setLayout("timeline"); setParallel(true); setCluster([]); }}>Параллельные</button>
        <button type="button" className={layout === "list" ? "chosen" : ""} onClick={() => setLayout("list")}>Список</button>
      </div>
      <div className="activity-legend"><span><i className="ok" /> Успешно</span><span><i className="error" /> Ошибка</span><span><i className="running" /> Выполняется</span></div>
    </div>
    {layout === "timeline" && !parallel && <p className="activity-compact-help">Одна компактная полоса на клиента. Близкие вызовы сгруппированы: нажмите на число, чтобы увидеть каждый. Полные длительности доступны в режиме «Параллельные».</p>}
    {cluster.length > 0 && <section className="card activity-cluster-detail" aria-label="Операции группы">
      <div><strong>{cluster.length} операций</strong><button type="button" onClick={() => setCluster([])}>Закрыть группу</button></div>
      {cluster.map((item) => <button key={item.id} className="activity-cluster-item" type="button" onClick={() => void select(item)}>
        <time>{timeLabel(item.time)}</time><strong>{item.tool}</strong><span>{item.target}</span>
      </button>)}
    </section>}
    <div className={selected ? "activity-content has-detail" : "activity-content"}>
      <section className="card activity-visual">
        {layout === "timeline" && <div className="activity-scroll-controls" aria-label="Горизонтальная прокрутка">
          <button type="button" aria-label="Таймлайн влево" disabled={scrollLeft <= 0} onClick={() => moveTimeline(scrollLeft - 400)}>←</button>
          <input type="range" aria-label="Положение таймлайна" min={0} max={scrollMax} value={scrollLeft} disabled={scrollMax === 0} onChange={(event) => moveTimeline(Number(event.target.value))} />
          <button type="button" aria-label="Таймлайн вправо" disabled={scrollLeft >= scrollMax} onClick={() => moveTimeline(scrollLeft + 400)}>→</button>
        </div>}
        {layout === "timeline" ? <div className="activity-scroll" ref={scrollRef} onScroll={(event) => setScrollLeft(event.currentTarget.scrollLeft)} tabIndex={0} aria-label="Прокручиваемый таймлайн операций">
          <div className="activity-board" style={{ width: width + 192 }}>
            <div className="activity-axis"><div className="activity-axis-client">Источник / клиент</div><div className="activity-axis-ticks" style={{ width }}>
              {Array.from({ length: 7 }, (_, i) => <span key={i} style={{ left: (i / 6 * 100) + "%" }}>{timeLabel(start + (now - start) * i / 6).slice(0, 5)}</span>)}
            </div></div>
            {lanes.map((lane) => <div className="activity-lane" key={lane.client} style={{ minHeight: parallel ? 20 + lane.tracks * 32 : 96 }}>
              <div className="activity-lane-label"><strong title={lane.client}>{labelClient(lane.client)}</strong><small>{lane.items.length} вызовов</small></div>
              <div className="activity-lane-track" style={{ width }}>
                {Array.from({ length: 7 }, (_, i) => <div className="activity-gridline" key={i} style={{ left: (i / 6 * 100) + "%" }} />)}
                {parallel ? lane.items.map(({ item, x, width: span, track }) => <button
                  key={item.id} className={"activity-mark " + item.outcome + (selected?.id === item.id ? " active" : "")}
                  style={{ left: x, top: 9 + track * 32, width: span }} type="button"
                  title={item.tool + " · " + timeLabel(item.time) + " · " + item.target}
                  aria-label={item.tool + " — " + labelClient(item.client) + " — " + timeLabel(item.time)}
                  onClick={() => void select(item)}>{span > 70 ? item.tool : ""}</button>) : compactGroups(lane).map((group) => <button
                  key={group.key} type="button" className={"activity-mark activity-cluster " + group.outcome}
                  style={{ left: group.x, top: 8 + (group.outcome === "ok" ? 0 : group.outcome === "error" ? 1 : 2) * 26, width: 24 }}
                  title={group.items.map((item) => item.tool + " · " + timeLabel(item.time)).join("\n")}
                  aria-label={group.items.length === 1 ? group.items[0].tool + " — " + labelClient(lane.client) + " — " + timeLabel(group.items[0].time) : group.items.length + " операций — " + labelClient(lane.client)}
                  onClick={() => { if (group.items.length === 1) { setCluster([]); void select(group.items[0]); } else setCluster(group.items); }}>
                  {group.items.length > 1 ? group.items.length : "·"}
                </button>)}
              </div>
            </div>)}
            {!lanes.length && <div className="activity-empty">По текущим фильтрам нет записанных операций.</div>}
          </div>
        </div> :
          <div className="activity-rows activity-request-table">
            <div className="activity-request-head" aria-hidden="true">
              <span>Время</span><span>Статус</span><span>Клиент</span><span>Инструмент / действие</span><span>Сервер</span><span>Длительность</span>
            </div>
            {operations.map((item) => <button className={"activity-request-row " + (selected?.id === item.id ? "active" : "")} key={item.id} type="button" onClick={() => void select(item)}>
              <time className="activity-request-time" title={dateLabel(item.time)}>{timeLabel(item.time)}</time>
              <span className={"activity-request-status " + item.outcome}>{field(item.event, "status") || (item.outcome === "running" ? "Выполняется" : "—")}</span>
              <span className="activity-request-client" title={item.client}>{labelClient(item.client)}</span>
              <span className="activity-request-tool">{item.tool}<small>{field(item.event, "trace_id") ? "Trace: " + field(item.event, "trace_id").slice(0, 16) : ""}</small></span>
              <span className="activity-request-target">{item.target}</span>
              <span className="activity-request-time">{item.end > item.time ? durationLabel(item.end - item.time) : "—"}</span>
            </button>)}
            {!operations.length && <div className="activity-empty">По текущим фильтрам нет записанных операций.</div>}
          </div>}
        <div className="activity-footer"><span>Отображается {operations.length} из {total} доступных по условиям. Длительность показывается только при наличии времени выполнения задачи.</span>
          {entries.length < total && <button type="button" className="button secondary" disabled={moreLoading || loading} onClick={() => void loadMore()}>{moreLoading ? "Загрузка…" : "Загрузить ещё"}</button>}
        </div>
      </section>
      {selected && <aside className="card activity-detail" aria-label="Детали операции">
        <div className="activity-detail-top"><span className={"activity-pill " + selected.outcome}>{selected.outcome === "ok" ? "Успешно" : selected.outcome === "running" ? "Выполняется" : "Ошибка / отказ"}</span><button type="button" className="text-button" onClick={() => { detailGeneration.current++; setSelected(null); }}>Закрыть ×</button></div>
        <h2>{selected.tool}</h2>
        <p className="muted-help">Данные реальной записи журнала Hub</p>
        <dl className="activity-facts">
          <div><dt>Кто вызывает</dt><dd>{labelClient(selected.client)}</dd></div>
          <div><dt>Откуда (actor)</dt><dd>{field(selected.event, "actor") || "Не записано"}</dd></div>
          <div><dt>Куда</dt><dd>{selected.target}</dd></div>
          <div><dt>Когда</dt><dd>{dateLabel(selected.time)}</dd></div>
          <div><dt>Длительность</dt><dd>{selected.end > selected.time ? durationLabel(selected.end - selected.time) : "Не измерялась"}</dd></div>
          <div><dt>HTTP-статус</dt><dd>{field(selected.event, "status") || "Не записан"}</dd></div>
          <div><dt>Решение политики</dt><dd>{field(selected.event, "policy_decision") || "—"}</dd></div>
          <div><dt>Профиль доступа</dt><dd>{field(selected.event, "profile_id") || "—"}</dd></div>
          <div><dt>Trace ID</dt><dd>{field(selected.event, "trace_id") || "—"}</dd></div>
          <div><dt>Correlation ID</dt><dd>{field(selected.event, "correlation_id") || "—"}</dd></div>
          <div><dt>Метод / endpoint</dt><dd>{[field(selected.event, "method"), field(selected.event, "endpoint")].filter(Boolean).join(" ") || "Не записано"}</dd></div>
          <div><dt>Поля аргументов</dt><dd>{Array.isArray(selected.event.fields?.argument_keys) ? (selected.event.fields.argument_keys as string[]).join(", ") || "Нет" : "Не записаны"}</dd></div>
          <div><dt>Хеш аргументов</dt><dd>{field(selected.event, "arguments_digest") || "—"}</dd></div>
          <div><dt>Задача / результат</dt><dd>{selected.jobId || "—"}</dd></div>
        </dl>
        {related.length > 0 && <section className="activity-related"><h3>Связанные запросы ({related.length})</h3><p className="muted-help">Одинаковый Trace ID, Correlation ID или задача. Только записи из загруженного периода.</p>{related.map((op) => <button key={op.id} className="activity-related-row" type="button" onClick={() => void select(op)}><time>{timeLabel(op.time)}</time><strong>{op.tool}</strong><small>{labelClient(op.client)}</small></button>)}</section>}
        <details className="activity-raw-details"><summary>Все поля записи</summary><pre>{JSON.stringify(selected.event, null, 2)}</pre><button className="button secondary" type="button" onClick={() => { void navigator.clipboard?.writeText(JSON.stringify(selected.event, null, 2)); }}>Копировать запись</button></details>
        {isJobId(selected.jobId) && <div className="activity-job-detail"><h3>Вызов и результат задачи</h3><p className="muted-help">Детали загружаются по ссылке из журнала. Секреты и отсутствующие аргументы не восстанавливаются.</p>
          {detailLoading ? <span>Загрузка…</span> : detailError ? <p role="alert">{detailError}</p> : <pre>{displayValue(jobDetail)}</pre>}
          <a className="text-button" href="#jobs">Открыть задачи</a>
        </div>}
        {!isJobId(selected.jobId) && <p className="muted-help">В журнале сохранён отпечаток аргументов, но не их исходное содержимое. Отдельной задачи для этого события нет.</p>}
      </aside>}
    </div>
    <p className="activity-note">Выбор клиентов сохраняется в этом браузере. Если источник не передаёт идентификатор клиента, он показывается как «Без установленного клиента». Журнал не является трассировщиком каждого внутреннего шага.</p>
  </div>;
}
