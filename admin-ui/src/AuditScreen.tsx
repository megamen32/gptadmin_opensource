import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";

type AuditEvent = { time?: string; name?: string; fields?: Record<string, unknown> };
type AuditResponse = { events?: AuditEvent[]; total?: number };

function displayField(value: unknown): string {
  if (value === null || value === undefined || value === "") return "—";
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return String(value);
  try { return JSON.stringify(value); } catch { return String(value); }
}

export default function AuditScreen() {
  const [query, setQuery] = useState("");
  const [appliedQuery, setAppliedQuery] = useState("");
  const [dimensions, setDimensions] = useState({ host: "", endpoint: "", server: "", consumer: "" });
  const [appliedDimensions, setAppliedDimensions] = useState(dimensions);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [total, setTotal] = useState(0);
  const [olderOffset, setOlderOffset] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const requestGeneration = useRef(0);

  const load = useCallback(async (signal?: AbortSignal) => {
    const generation = ++requestGeneration.current;
    setLoading(true);
    setError(null);
    try {
      const params = new URLSearchParams({ limit: "500", offset: "0" });
      if (appliedQuery.trim()) params.set("q", appliedQuery.trim());
      const response = await fetch(`/admin/api/audit?${params}`, { credentials: "same-origin", signal, headers: { Accept: "application/json" } });
      if (!response.ok) throw new Error(`Hub вернул HTTP ${response.status}`);
      const data = await response.json() as AuditResponse;
      if (signal?.aborted || generation !== requestGeneration.current) return;
      let next = Array.isArray(data.events) ? data.events : [];
      const eventTotal = typeof data.total === "number" ? data.total : next.length;
      let latestOffset = 0;
      if (eventTotal > next.length) {
        latestOffset = Math.max(0, eventTotal - 500);
        const latestParams = new URLSearchParams({ limit: "500", offset: String(latestOffset) });
        if (appliedQuery.trim()) latestParams.set("q", appliedQuery.trim());
        const latestResponse = await fetch(`/admin/api/audit?${latestParams}`, { credentials: "same-origin", signal, headers: { Accept: "application/json" } });
        if (!latestResponse.ok) throw new Error(`Hub вернул HTTP ${latestResponse.status}`);
        const latestData = await latestResponse.json() as AuditResponse;
        if (signal?.aborted || generation !== requestGeneration.current) return;
        if (Array.isArray(latestData.events)) next = latestData.events;
      }
      setEvents(next);
      setTotal(eventTotal);
      setOlderOffset(latestOffset > 0 ? latestOffset : null);
    } catch (caught) {
      if (!signal?.aborted && generation === requestGeneration.current && (caught as DOMException).name !== "AbortError") {
        setError(caught instanceof Error ? caught.message : "Не удалось загрузить журнал");
      }
    } finally {
      if (!signal?.aborted && generation === requestGeneration.current) setLoading(false);
    }
  }, [appliedQuery]);

  useEffect(() => {
    const controller = new AbortController();
    setEvents([]);
    setTotal(0);
    setOlderOffset(null);
    void load(controller.signal);
    return () => {
      controller.abort();
      requestGeneration.current += 1;
    };
  }, [load]);

  const loadOlder = useCallback(async () => {
    if (olderOffset === null) return;
    const generation = requestGeneration.current;
    const limit = Math.min(500, olderOffset);
    const offset = olderOffset - limit;
    setLoading(true);
    setError(null);
    try {
      const params = new URLSearchParams({ limit: String(limit), offset: String(offset) });
      if (appliedQuery.trim()) params.set("q", appliedQuery.trim());
      const response = await fetch(`/admin/api/audit?${params}`, { credentials: "same-origin", headers: { Accept: "application/json" } });
      if (!response.ok) throw new Error(`Hub вернул HTTP ${response.status}`);
      const data = await response.json() as AuditResponse;
      if (generation !== requestGeneration.current) return;
      setEvents((current) => [...current, ...(Array.isArray(data.events) ? data.events : [])]);
      setOlderOffset(offset > 0 ? offset : null);
    } catch (caught) {
      if (generation === requestGeneration.current) setError(caught instanceof Error ? caught.message : "Не удалось загрузить журнал");
    } finally {
      if (generation === requestGeneration.current) setLoading(false);
    }
  }, [olderOffset, appliedQuery]);

  const filteredEvents = useMemo(() => {
    const valueFor = (fields: Record<string, unknown>, keys: string[]) => keys.map((key) => fields[key]).filter((value) => value !== null && value !== undefined && value !== "").map(displayField).join(" ").toLocaleLowerCase();
    return events.filter((event) => {
      const fields = event.fields ?? {};
      if (appliedDimensions.host && !valueFor(fields, ["host"]).includes(appliedDimensions.host.trim().toLocaleLowerCase())) return false;
      if (appliedDimensions.endpoint && !valueFor(fields, ["path", "endpoint", "tool"]).includes(appliedDimensions.endpoint.trim().toLocaleLowerCase())) return false;
      if (appliedDimensions.server && !valueFor(fields, ["target", "server_id", "agent_id"]).includes(appliedDimensions.server.trim().toLocaleLowerCase())) return false;
      if (appliedDimensions.consumer && !valueFor(fields, ["client_id", "actor", "subject"]).includes(appliedDimensions.consumer.trim().toLocaleLowerCase())) return false;
      return true;
    }).sort((a, b) => Date.parse(b.time ?? "") - Date.parse(a.time ?? ""));
  }, [events, appliedDimensions]);

  const submit = (event: FormEvent) => { event.preventDefault(); setAppliedQuery(query); setAppliedDimensions(dimensions); };
  const shownFields = useMemo(() => filteredEvents.map((event) => {
    const fields = event.fields ?? {};
    const entries = Object.entries(fields);
    const preferred = ["host", "path", "endpoint", "target", "server_id", "agent_id", "client_id", "actor", "method", "status"];
    const result = preferred.flatMap((key) => entries.filter(([field]) => field === key)).slice(0, 6);
    for (const entry of entries) {
      if (result.length === 6) break;
      if (!result.some(([key]) => key === entry[0])) result.push(entry);
    }
    return result;
  }), [filteredEvents]);

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="ЖУРНАЛ" title="Журнал" description="События доступа, политик и операций без сырого шума интерфейса." actions={<StatusBadge state={error ? "error" : "ready"}>{loading ? "Загрузка…" : `${filteredEvents.length} / ${events.length}${total > events.length ? ` из ${total} всего` : ""}`}</StatusBadge>} />
    <form className="audit-toolbar card" onSubmit={submit}>
      <label><span>Поиск</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="событие, actor, target…" /></label>
      <button className="button primary" type="submit">Найти</button>
      <button className="button secondary" type="button" onClick={() => { const empty = { host: "", endpoint: "", server: "", consumer: "" }; setQuery(""); setAppliedQuery(""); setDimensions(empty); setAppliedDimensions(empty); }}>Сбросить</button>
      <details className="audit-dimension-filters" open style={{ gridColumn: "1 / -1" }}>
        <summary>Фильтры по полям · все заполненные условия обязательны</summary>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(190px, 1fr))", gap: 10, marginTop: 10 }}>
          <label><span>Хост MCP</span><input value={dimensions.host} onChange={(event) => setDimensions((current) => ({ ...current, host: event.target.value }))} placeholder="Например, mcp.example" /></label>
          <label><span>Endpoint</span><input value={dimensions.endpoint} onChange={(event) => setDimensions((current) => ({ ...current, endpoint: event.target.value }))} placeholder="Путь запроса или MCP tool" /></label>
          <label><span>Сервер</span><input value={dimensions.server} onChange={(event) => setDimensions((current) => ({ ...current, server: event.target.value }))} placeholder="target / server_id / agent_id" /></label>
          <label><span>Потребитель</span><input value={dimensions.consumer} onChange={(event) => setDimensions((current) => ({ ...current, consumer: event.target.value }))} placeholder="client_id / actor / subject" /></label>
        </div>
      </details>
    </form>
    <p className="muted-help audit-scope-note">Поиск текста проверяется по всей удерживаемой истории Hub. Фильтры по полям применяются к загруженным событиям ({events.length}); для более ранних совпадений используйте кнопку загрузки ниже.</p>
    <ErrorNotice message={error} />
    <section className="card audit-native-list">
      {filteredEvents.length === 0 && !loading ? <div className="compact-empty"><strong>Событий нет</strong><span>По текущему фильтру ничего не найдено.</span></div> : filteredEvents.map((event, index) => <article className="audit-native-row" key={`${event.time}-${event.name}-${index}`}>
        <div className="audit-native-head"><span className="job-state">{event.name || "event"}</span><time>{event.time || "—"}</time></div>
        <div className="audit-native-fields">{shownFields[index].map(([key, value]) => <div key={key}><span>{key}</span><strong>{displayField(value)}</strong></div>)}</div>
        {Object.keys(event.fields ?? {}).length > shownFields[index].length && <details className="audit-event-details"><summary>Подробности события {event.name || "event"}</summary><dl className="meta-grid">{Object.entries(event.fields ?? {}).map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{displayField(value)}</dd></div>)}</dl></details>}
      </article>)}
    </section>
    {olderOffset !== null && <div className="load-more-row"><button className="button secondary" type="button" disabled={loading} onClick={() => void loadOlder()}>{loading ? "Загрузка…" : "Показать более ранние записи"}</button></div>}
  </div>;
}
