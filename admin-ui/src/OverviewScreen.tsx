import { useCallback, useEffect, useMemo, useState } from "react";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";
import { friendlyActionName, friendlyServerName, jobStatusLabel, serverStatusLabel } from "./ui/format";

type Server = {
  server_id?: string;
  name?: string;
  status?: string;
  kind?: string;
  last_seen?: string;
};

type Job = {
  job_id?: string;
  status?: string;
  tool_name?: string;
  command?: string;
  arguments_preview?: string;
  result_preview?: string;
  error_preview?: string;
  created_at?: number | string;
};

type Overview = {
  server_counts?: { online?: number; offline?: number; stale?: number };
  client_count?: number;
  servers?: Server[];
  jobs?: { recent?: Job[]; queued?: Job[]; background?: Job[]; count?: number };
  build?: { build_version?: string | number; git_commit?: string };
  shell_builds?: { versions?: Record<string, number> };
  update?: { current?: { status?: string }; last_result?: { status?: string; message?: string } | null };
  now_fmt?: string;
  hub_public_url?: string;
  public_origin?: string;
};

function compactTime(value: number | string | undefined): string {
  if (value === undefined || value === "") return "—";
  const numeric = typeof value === "number" ? value : Number(value);
  const date = Number.isFinite(numeric) ? new Date(numeric < 1e12 ? numeric * 1000 : numeric) : new Date(value);
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
}

async function requestOverview(signal?: AbortSignal): Promise<Overview> {
  const response = await fetch("/admin/api/overview?limit=24", { credentials: "same-origin", signal, headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(`Не удалось получить состояние системы (HTTP ${response.status})`);
  return response.json() as Promise<Overview>;
}

export default function OverviewScreen() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(true);
  const [updating, setUpdating] = useState(false);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      setRefreshing(true);
      const next = await requestOverview(signal);
      setOverview(next);
      setError(null);
    } catch (caught) {
      if ((caught as DOMException).name !== "AbortError") setError(caught instanceof Error ? caught.message : "Не удалось загрузить обзор");
    } finally {
      if (!signal?.aborted) setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal);
    const timer = window.setInterval(() => void refresh(controller.signal), 15_000);
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [refresh]);

  const servers = overview?.servers ?? [];
  const recentJobs = overview?.jobs?.recent ?? [];
  const problems = useMemo(() => servers.filter((server) => server.status !== "online").slice(0, 6), [servers]);
  const failedJobs = useMemo(() => recentJobs.filter((job) => job.status === "failed" || job.status === "error").slice(0, 6), [recentJobs]);
  const activeJobs = useMemo(() => recentJobs.filter((job) => job.status === "running" || String(job.status || "").startsWith("queued")).slice(0, 4), [recentJobs]);
  const counts = overview?.server_counts ?? {};
  const queuedCount = overview?.jobs?.queued?.length ?? 0;
  const backgroundCount = overview?.jobs?.background?.length ?? 0;
  const update = overview?.update;
  const updateRunning = updating || update?.current?.status === "running";
  const publicUrl = overview?.hub_public_url || overview?.public_origin;
  const shellVersions = Object.entries(overview?.shell_builds?.versions ?? {}).map(([version, count]) => `${version}×${count}`).join(", ") || "—";

  const health = error ? "error" : problems.length > 0 || failedJobs.length > 0 ? "warning" : "healthy";
  const healthTitle = health === "error" ? "Не удалось проверить систему" : health === "warning" ? "Есть то, что требует внимания" : "Всё работает нормально";
  const healthText = health === "error"
    ? "Связь с системой временно недоступна. Попробуйте обновить страницу."
    : health === "warning"
      ? `${problems.length ? `${problems.length} сервер${problems.length === 1 ? " требует" : "а требуют"} внимания. ` : ""}${failedJobs.length ? `${failedJobs.length} последн${failedJobs.length === 1 ? "яя задача завершилась" : "их задач завершились"} с ошибкой.` : ""}`
      : "Серверы отвечают, критических ошибок в последних задачах нет.";

  const triggerUpdate = async () => {
    setUpdating(true);
    setError(null);
    try {
      const response = await fetch("/admin/api/update", { method: "POST", credentials: "same-origin", headers: { Accept: "application/json", "Content-Type": "application/json" }, body: "{}" });
      if (!response.ok) throw new Error(`Обновление не запущено (HTTP ${response.status})`);
      await refresh();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Не удалось запустить обновление");
    } finally {
      setUpdating(false);
    }
  };

  return (
    <div className="page-shell overview-page">
      <PageHeader
        eyebrow="СОСТОЯНИЕ СИСТЕМЫ"
        title="Обзор"
        description="Здесь показано только то, что важно прямо сейчас."
        actions={<><StatusBadge state={error ? "error" : "ready"}>{refreshing ? "Обновляем…" : error ? "Нет связи" : "Данные актуальны"}</StatusBadge><button className="button secondary" type="button" onClick={() => void refresh()} disabled={refreshing}>Обновить</button></>}
      />

      <ErrorNotice message={error} />

      <section className={`health-hero health-${health}`} aria-live="polite">
        <div className="health-icon" aria-hidden="true">{health === "healthy" ? "✓" : health === "warning" ? "!" : "×"}</div>
        <div className="health-copy"><p className="section-kicker">СЕЙЧАС</p><h2>{healthTitle}</h2><p>{healthText}</p></div>
        {health === "warning" && <div className="health-actions">{problems.length > 0 && <a className="button secondary" href="#agents">Проверить серверы</a>}{failedJobs.length > 0 && <a className="button secondary" href="#jobs">Посмотреть ошибки</a>}</div>}
      </section>

      <section className="overview-summary-strip" aria-label="Краткое состояние">
        <a href="#agents"><span>Серверы</span><strong>{counts.online ?? 0} работают</strong><small>{(counts.offline ?? 0) + (counts.stale ?? 0) > 0 ? `${(counts.offline ?? 0) + (counts.stale ?? 0)} требуют внимания` : "проблем нет"}</small></a>
        <a href="#jobs"><span>Задачи</span><strong>{backgroundCount} выполняются</strong><small>{queuedCount > 0 ? `${queuedCount} ожидают запуска` : "очередь свободна"}</small></a>
        <a href="#clients"><span>Доступ</span><strong>{overview?.client_count ?? 0} клиентов</strong><small>управление доступом</small></a>
      </section>

      {(problems.length > 0 || failedJobs.length > 0) && <section className="attention-stack" aria-label="Требует внимания">
        {problems.length > 0 && <article className="card overview-panel attention-panel">
          <div className="card-heading"><div><p className="section-kicker">ТРЕБУЕТ ВНИМАНИЯ</p><h2>Серверы</h2></div><span className="count-pill">{problems.length}</span></div>
          <div className="compact-list">{problems.map((server) => <a className="compact-row" href="#agents" key={server.server_id ?? server.name}><span className={`status-dot status-${server.status ?? "unknown"}`} /><span><strong>{friendlyServerName(server.server_id, server.name)}</strong><small>{serverStatusLabel(server.status)}{server.last_seen ? ` · последний ответ ${server.last_seen}` : ""}</small></span></a>)}</div>
        </article>}

        {failedJobs.length > 0 && <article className="card overview-panel attention-panel">
          <div className="card-heading"><div><p className="section-kicker">ОШИБКИ</p><h2>Последние задачи</h2></div><a className="text-button" href="#jobs">Все задачи</a></div>
          <div className="compact-list">{failedJobs.map((job) => <a className="compact-row" href="#jobs" key={job.job_id}><span className={`job-state job-${job.status ?? "unknown"}`}>{jobStatusLabel(job.status)}</span><span><strong>{friendlyActionName(job.tool_name)}</strong><small>{job.error_preview || job.command || job.arguments_preview || "Подробности доступны в задачах"} · {compactTime(job.created_at)}</small></span></a>)}</div>
        </article>}
      </section>}

      {activeJobs.length > 0 && <section className="card overview-panel current-work-panel">
        <div className="card-heading"><div><p className="section-kicker">СЕЙЧАС В РАБОТЕ</p><h2>Активные задачи</h2></div><a className="text-button" href="#jobs">Все задачи</a></div>
        <div className="compact-list">{activeJobs.map((job) => <a className="compact-row" href="#jobs" key={job.job_id}><span className={`job-state job-${job.status ?? "unknown"}`}>{jobStatusLabel(job.status)}</span><span><strong>{friendlyActionName(job.tool_name)}</strong><small>{compactTime(job.created_at)}</small></span></a>)}</div>
      </section>}

      <details className="card overview-system-card overview-tech-details"><summary>Техническая информация</summary>
        <div className="card-heading"><div><p className="section-kicker">ТЕХНИЧЕСКАЯ ИНФОРМАЦИЯ</p><h2>Версии и обновление</h2></div><button className="button primary" type="button" onClick={() => void triggerUpdate()} disabled={updateRunning}>{updateRunning ? "Обновляем…" : "Обновить этот узел"}</button></div>
        <div className="system-facts">
          <div><span>Главный сервис</span><strong>версия {overview?.build?.build_version ?? "—"}</strong><small>{overview?.build?.git_commit?.slice(0, 7) || "версия кода —"}</small></div>
          <div><span>Агенты</span><strong>{shellVersions}</strong><small>версии агентов</small></div>
          <div><span>Адрес системы</span><strong>{publicUrl ? <a href={publicUrl} target="_blank" rel="noreferrer">{publicUrl}</a> : "—"}</strong><small>{overview?.now_fmt || "обновление каждые 15 секунд"}</small></div>
        </div>
        {update?.last_result?.message && <div className={`inline-note ${update.last_result.status === "error" ? "danger-note" : ""}`}>{update.last_result.message}</div>}
      </details>
    </div>
  );
}
