import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";

type AuditEvent = { time?: string; name?: string; fields?: Record<string, unknown> };
type AuditResponse = { events?: AuditEvent[]; total?: number; next_offset?: number };

function displayField(value: unknown): string {
  if (value === null || value === undefined || value === "") return "—";
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return String(value);
  try { return JSON.stringify(value); } catch { return String(value); }
}

export default function AuditScreen() {
  const [query, setQuery] = useState("");
  const [appliedQuery, setAppliedQuery] = useState("");
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [total, setTotal] = useState(0);
  const [nextOffset, setNextOffset] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (offset = 0, append = false, signal?: AbortSignal) => {
    setLoading(true);
    setError(null);
    try {
      const params = new URLSearchParams({ limit: "80", offset: String(offset) });
      if (appliedQuery.trim()) params.set("q", appliedQuery.trim());
      const response = await fetch(`/admin/api/audit?${params}`, { credentials: "same-origin", signal, headers: { Accept: "application/json" } });
      if (!response.ok) throw new Error(`Hub вернул HTTP ${response.status}`);
      const data = await response.json() as AuditResponse;
      const next = Array.isArray(data.events) ? data.events : [];
      setEvents((current) => append ? [...current, ...next] : next);
      setTotal(typeof data.total === "number" ? data.total : next.length);
      setNextOffset(typeof data.next_offset === "number" ? data.next_offset : null);
    } catch (caught) {
      if ((caught as DOMException).name !== "AbortError") setError(caught instanceof Error ? caught.message : "Не удалось загрузить журнал");
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [appliedQuery]);

  useEffect(() => {
    const controller = new AbortController();
    void load(0, false, controller.signal);
    return () => controller.abort();
  }, [load]);

  const submit = (event: FormEvent) => { event.preventDefault(); setAppliedQuery(query); };
  const shownFields = useMemo(() => events.map((event) => Object.entries(event.fields ?? {}).slice(0, 6)), [events]);

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="ЖУРНАЛ" title="Журнал" description="События доступа, политик и операций без сырого шума интерфейса." actions={<StatusBadge state={error ? "error" : "ready"}>{loading ? "Загрузка…" : `${events.length} / ${total}`}</StatusBadge>} />
    <form className="audit-toolbar card" onSubmit={submit}>
      <label><span>Поиск</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="событие, actor, target…" /></label>
      <button className="button primary" type="submit">Найти</button>
      <button className="button secondary" type="button" onClick={() => { setQuery(""); setAppliedQuery(""); }}>Сбросить</button>
    </form>
    <ErrorNotice message={error} />
    <section className="card audit-native-list">
      {events.length === 0 && !loading ? <div className="compact-empty"><strong>Событий нет</strong><span>По текущему фильтру ничего не найдено.</span></div> : events.map((event, index) => <article className="audit-native-row" key={`${event.time}-${event.name}-${index}`}>
        <div className="audit-native-head"><span className="job-state">{event.name || "event"}</span><time>{event.time || "—"}</time></div>
        <div className="audit-native-fields">{shownFields[index].map(([key, value]) => <div key={key}><span>{key}</span><strong>{displayField(value)}</strong></div>)}</div>
      </article>)}
    </section>
    {nextOffset !== null && <div className="load-more-row"><button className="button secondary" type="button" disabled={loading} onClick={() => void load(nextOffset, true)}>{loading ? "Загрузка…" : "Показать ещё"}</button></div>}
  </div>;
}
