import { useCallback, useEffect, useMemo, useState } from "react";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";
import { friendlyServerName, serverStatusLabel } from "./ui/format";

type Server = {
  server_id?: string;
  name?: string;
  status?: string;
  kind?: string;
  transport?: string;
  last_seen?: string;
  capabilities?: string[];
  meta?: Record<string, unknown>;
};

type Overview = { servers?: Server[] };


function textValue(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return String(value);
  try { return JSON.stringify(value); } catch { return String(value); }
}

export default function AgentsScreen() {
  const [servers, setServers] = useState<Server[]>([]);
  const [filter, setFilter] = useState("");
  const [status, setStatus] = useState("all");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

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
    if (status !== "all" && server.status !== status) return false;
    if (!filter.trim()) return true;
    return JSON.stringify(server).toLowerCase().includes(filter.trim().toLowerCase());
  }), [servers, filter, status]);
  const selected = servers.find((server) => server.server_id === selectedId) ?? null;

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="СЕРВЕРЫ" title="Серверы" description="Какие серверы работают, какие недоступны и когда они отвечали в последний раз." actions={<><StatusBadge state={error ? "error" : "ready"}>{loading ? "Обновляем…" : `${filtered.length} узлов`}</StatusBadge><button className="button secondary" type="button" disabled={loading} onClick={() => void load()}>Обновить</button></>} />
    <ErrorNotice message={error} />
    <section className={`agent-native-layout ${selected ? "has-detail" : ""}`}>
      <article className="card agent-native-list">
        <div className="list-toolbar"><input className="search" value={filter} onChange={(event) => setFilter(event.target.value)} placeholder="Фильтр серверов" /><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="all">Все статусы</option><option value="online">Работают</option><option value="offline">Недоступны</option><option value="stale">Давно не отвечают</option></select></div>
        {filtered.length === 0 && !loading ? <div className="compact-empty"><strong>Серверов не найдено</strong><span>Измените фильтр или обновите данные.</span></div> : <div className="compact-list">{filtered.map((server) => <button type="button" className={`server-native-row ${selectedId === server.server_id ? "selected" : ""}`} key={server.server_id || server.name} onClick={() => setSelectedId(server.server_id || null)}><span className={`status-dot status-${server.status || "unknown"}`} /><span className="server-native-main"><strong>{friendlyServerName(server.server_id, server.name)}</strong><small>{server.last_seen ? `Последний ответ: ${server.last_seen}` : "Ожидаем данные о последнем ответе"}</small></span><span className="server-native-status">{serverStatusLabel(server.status)}</span></button>)}</div>}
      </article>
      {selected && <aside className="card agent-detail-pane">
        <div className="card-heading"><div><p className="section-kicker">ПОДРОБНОСТИ</p><h2>{friendlyServerName(selected.server_id, selected.name)}</h2></div><button className="text-button" type="button" onClick={() => setSelectedId(null)}>Закрыть</button></div>
        <dl className="detail-facts"><div><dt>Технический ID</dt><dd>{selected.server_id || "—"}</dd></div><div><dt>Состояние</dt><dd>{serverStatusLabel(selected.status)}</dd></div><div><dt>Тип подключения</dt><dd>{selected.kind || "—"}</dd></div><div><dt>Транспорт</dt><dd>{selected.transport || "—"}</dd></div><div><dt>Последний ответ</dt><dd>{selected.last_seen || "—"}</dd></div></dl>
        <details className="detail-section technical-details"><summary>Технические возможности</summary><div className="pill-wrap">{selected.capabilities?.length ? selected.capabilities.map((cap) => <span className="small-pill" key={cap}>{cap}</span>) : <span className="muted-help">Не переданы</span>}</div></details>
        <details className="detail-section technical-details"><summary>Метаданные</summary>{Object.keys(selected.meta ?? {}).length ? <dl className="meta-grid">{Object.entries(selected.meta ?? {}).map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{textValue(value)}</dd></div>)}</dl> : <p className="muted-help">Нет метаданных.</p>}</details>
      </aside>}
    </section>
  </div>;
}
