import { useCallback, useEffect, useMemo, useState } from "react";
import { friendlyActionName, friendlyServerName, jobStatusLabel } from "./ui/format";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";

type Job = {
  job_id?: string;
  task_id?: string;
  status?: string;
  tool_name?: string;
  command?: string;
  arguments_preview?: string;
  result_preview?: string;
  error_preview?: string;
  server_id?: string;
  server?: string;
  created_at?: number | string;
};

type Overview = { jobs?: { recent?: Job[]; queued?: Job[]; background?: Job[]; count?: number } };

function compactTime(value: number | string | undefined): string {
  if (value === undefined || value === "") return "—";
  const numeric = typeof value === "number" ? value : Number(value);
  const date = Number.isFinite(numeric) ? new Date(numeric < 1e12 ? numeric * 1000 : numeric) : new Date(value);
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString("ru-RU", { dateStyle: "short", timeStyle: "medium" });
}

function jobInput(job: Job): string { return job.command || job.arguments_preview || "Входные данные не сохранены"; }
function jobPreview(job: Job): string { return job.error_preview || job.result_preview || ""; }

export default function JobsScreen() {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [total, setTotal] = useState(0);
  const [filter, setFilter] = useState("");
  const [status, setStatus] = useState("all");
  const [selected, setSelected] = useState<Job | null>(null);
  const [detail, setDetail] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      setLoading(true);
      const response = await fetch("/admin/api/overview?limit=160", { credentials: "same-origin", signal, headers: { Accept: "application/json" } });
      if (!response.ok) throw new Error(`Hub вернул HTTP ${response.status}`);
      const data = await response.json() as Overview;
      const recent = Array.isArray(data.jobs?.recent) ? data.jobs!.recent! : [];
      setJobs(recent);
      setTotal(typeof data.jobs?.count === "number" ? data.jobs.count : recent.length);
      setError(null);
    } catch (caught) {
      if ((caught as DOMException).name !== "AbortError") setError(caught instanceof Error ? caught.message : "Не удалось загрузить задачи");
    } finally { if (!signal?.aborted) setLoading(false); }
  }, []);

  useEffect(() => { const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load]);

  const filtered = useMemo(() => jobs.filter((job) => {
    if (status === "queued" && !String(job.status || "").startsWith("queued")) return false;
    if (status !== "all" && status !== "queued" && job.status !== status) return false;
    if (!filter.trim()) return true;
    return JSON.stringify(job).toLowerCase().includes(filter.trim().toLowerCase());
  }), [jobs, filter, status]);

  const open = async (job: Job) => {
    setSelected(job); setDetail(null); setDetailLoading(true); setError(null);
    try {
      if (!job.job_id) throw new Error("У задачи нет job_id");
      const response = await fetch(`/mcp-relay/job/${encodeURIComponent(job.job_id)}?detail=full`, { credentials: "same-origin", headers: { Accept: "application/json" } });
      const text = await response.text();
      let data: unknown = text;
      try { data = text ? JSON.parse(text) : {}; } catch { /* keep text */ }
      if (!response.ok) throw new Error(`Hub вернул HTTP ${response.status}`);
      setDetail(data);
    } catch (caught) { setError(caught instanceof Error ? caught.message : "Не удалось получить задачу"); }
    finally { setDetailLoading(false); }
  };

  const loadMore = async () => {
    setLoading(true); setError(null);
    try {
      const response = await fetch(`/admin/api/jobs?offset=${jobs.length}&limit=200`, { credentials: "same-origin", headers: { Accept: "application/json" } });
      if (!response.ok) throw new Error(`Hub вернул HTTP ${response.status}`);
      const data = await response.json() as { recent?: Job[]; count?: number };
      const next = Array.isArray(data.recent) ? data.recent : [];
      setJobs((current) => [...new Map([...current, ...next].map((job) => [job.job_id || `${job.created_at}-${job.tool_name}`, job])).values()]);
      if (typeof data.count === "number") setTotal(data.count);
    } catch (caught) { setError(caught instanceof Error ? caught.message : "Не удалось загрузить ещё задачи"); }
    finally { setLoading(false); }
  };

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="ЗАДАЧИ" title="Задачи" description="Очередь, история и полный результат выполнения в одном экране." actions={<><StatusBadge state={error ? "error" : "ready"}>{loading ? "Обновляем…" : `${jobs.length} / ${total}`}</StatusBadge><button className="button secondary" type="button" onClick={() => void load()} disabled={loading}>Обновить</button></>} />
    <ErrorNotice message={error} />
    <section className={`job-native-layout ${selected ? "has-detail" : ""}`}>
      <article className="card job-native-list">
        <div className="list-toolbar"><input className="search" value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="Фильтр задач" /><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="all">Все статусы</option><option value="running">Выполняются</option><option value="queued">В очереди</option><option value="completed">Готовы</option><option value="failed">С ошибкой</option></select></div>
        <div className="compact-list">{filtered.map((job) => <button className={`job-native-row ${selected?.job_id === job.job_id ? "selected" : ""}`} type="button" key={job.job_id || `${job.created_at}-${job.tool_name}`} onClick={() => void open(job)}><span className={`job-state job-${job.status || "unknown"}`}>{jobStatusLabel(job.status)}</span><span className="job-native-main"><strong>{friendlyActionName(job.tool_name)}</strong><small>{friendlyServerName(job.server_id || job.server, job.server)} · {compactTime(job.created_at)}</small><code>{jobInput(job)}</code>{jobPreview(job) && <em>{jobPreview(job)}</em>}</span></button>)}</div>
        {jobs.length < total && <div className="load-more-row"><button className="button secondary" type="button" disabled={loading} onClick={() => void loadMore()}>Показать ещё</button></div>}
      </article>
      {selected && <aside className="card job-detail-pane"><div className="card-heading"><div><p className="section-kicker">ПОДРОБНОСТИ ЗАДАЧИ</p><h2>{selected.job_id || "Задача"}</h2></div><button className="text-button" type="button" onClick={() => { setSelected(null); setDetail(null); }}>Закрыть</button></div><dl className="detail-facts"><div><dt>Состояние</dt><dd>{jobStatusLabel(selected.status)}</dd></div><div><dt>Действие</dt><dd>{friendlyActionName(selected.tool_name)}</dd></div><div><dt>Сервер</dt><dd>{friendlyServerName(selected.server_id || selected.server, selected.server)}</dd></div><div><dt>Создана</dt><dd>{compactTime(selected.created_at)}</dd></div></dl><div className="detail-section"><h3>Входные данные</h3><pre className="detail-code">{jobInput(selected)}</pre></div><div className="detail-section"><h3>Полный результат</h3><pre className="raw-native-box job-full-result">{detailLoading ? "Загрузка…" : detail === null ? "—" : JSON.stringify(detail, null, 2)}</pre></div></aside>}
    </section>
  </div>;
}
