import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";
import "./ConsoleScreen.css";

type ConsoleEntry = { time: string; level: "error" | "warn" | "info" | "debug"; message: string; unit: string };
type ConsoleResponse = { entries: ConsoleEntry[]; unit: string; count: number };
type LogLevel = "all" | ConsoleEntry["level"];

function displayTime(time: string) {
  const date = new Date(time);
  return !Number.isNaN(date.getTime()) ? date.toLocaleTimeString("ru-RU", { hour12: false }) : "—";
}

export default function ConsoleScreen() {
  const [entries, setEntries] = useState<ConsoleEntry[]>([]);
  const [level, setLevel] = useState<LogLevel>("all");
  const [search, setSearch] = useState("");
  const [follow, setFollow] = useState(true);
  const [live, setLive] = useState(true);
  const [unit, setUnit] = useState("gptadmin-hub.service");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [updated, setUpdated] = useState<string | null>(null);
  const logRef = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);
  const generation = useRef(0);

  const load = useCallback(async (signal?: AbortSignal) => {
    const ticket = ++generation.current;
    try {
      const response = await fetch("/admin/api/console?limit=500", { credentials: "same-origin", signal });
      if (!response.ok) throw new Error(response.status === 404
        ? "Эта версия Hub не поддерживает консоль. Требуется публикация обновления."
        : "Не удалось прочитать журнал сервиса (HTTP " + response.status + ")");
      const data = await response.json() as ConsoleResponse;
      if (ticket !== generation.current || signal?.aborted) return;
      setEntries(Array.isArray(data.entries) ? data.entries : []);
      if (typeof data.unit === "string") setUnit(data.unit);
      setUpdated(new Date().toLocaleTimeString("ru-RU", { hour12: false }));
      setError(null);
    } catch (caught) {
      if (ticket === generation.current && !signal?.aborted && (caught as Error).name !== "AbortError") {
        setError(caught instanceof Error ? caught.message : "Ошибка чтения консоли");
      }
    } finally {
      if (ticket === generation.current && !signal?.aborted) setLoading(false);
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    if (!live) return () => { controller.abort(); generation.current++; };
    const interval = window.setInterval(() => void load(controller.signal), 5000);
    return () => { clearInterval(interval); controller.abort(); generation.current++; };
  }, [load, live]);

  const filtered = useMemo(() => entries.filter((entry) =>
    (level === "all" || entry.level === level) &&
    (!search.trim() || (entry.message + " " + entry.time + " " + entry.level).toLowerCase().includes(search.toLowerCase().trim()))
  ), [entries, level, search]);

  useEffect(() => {
    if (follow && pinned.current && logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [filtered, follow]);

  return <div className="page-shell console-page">
    <PageHeader eyebrow="АКТИВНОСТЬ · КОНСОЛЬ" title="Логи приложения"
      description="Технические сообщения самого GPT Admin Hub из systemd journal. Это не журнал пользовательских вызовов."
      actions={<StatusBadge state={error ? "error" : "ready"}>{loading ? "Загрузка…" : live ? "Обновляется · 5 с" : "Обновление выключено"}</StatusBadge>} />
    <div className="console-toolbar card">
      <label>Уровень<select aria-label="Уровень логов" value={level} onChange={(event) => setLevel(event.target.value as LogLevel)}>
        <option value="all">Все уровни</option><option value="error">Ошибки</option><option value="warn">Предупреждения</option><option value="info">Информация</option><option value="debug">Отладка</option>
      </select></label>
      <label className="console-search">Поиск<input aria-label="Поиск по консоли" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Фрагмент сообщения…" /></label>
      <label className="console-check"><input type="checkbox" checked={follow} onChange={(event) => { setFollow(event.target.checked); pinned.current = true; }} /> Следовать за новыми</label>
      <label className="console-check"><input type="checkbox" checked={live} onChange={(event) => setLive(event.target.checked)} /> Автообновление</label>
      <button className="button secondary" type="button" onClick={() => void load()}>Обновить</button>
    </div>
    <div className="console-meta"><span>{unit}</span><span>{filtered.length} / {entries.length} сообщений</span><span>{updated ? "Обновлено " + updated : "Нет обновлений"}</span></div>
    <ErrorNotice message={error} />
    <section className="console-terminal card" aria-label="Консоль GPT Admin">
      <div className="console-terminal-title"><span className="console-lights" aria-hidden="true"><i /><i /><i /></span> GPT Admin · Application console</div>
      <div className="console-lines" role="log" aria-live="off" ref={logRef} onScroll={() => {
        const box = logRef.current;
        if (box) pinned.current = box.scrollHeight - box.scrollTop - box.clientHeight < 36;
      }}>
        {filtered.map((entry, index) => <div key={index + ":" + entry.time + ":" + entry.message} className="console-line">
          <time title={entry.time}>{displayTime(entry.time)}</time>
          <span className={"console-level " + entry.level}>{entry.level.toUpperCase()}</span>
          <span className="console-message">{entry.message}</span>
          <button className="console-copy" title="Копировать сообщение" aria-label={"Копировать строку " + (index + 1)} type="button"
            onClick={() => { void navigator.clipboard?.writeText(entry.message); }}>Копировать</button>
        </div>)}
        {!filtered.length && <div className="console-empty">{error ? "Не удалось загрузить события." : loading ? "Чтение журнала…" : "По выбранным условиям сообщений нет."}</div>}
      </div>
    </section>
    <p className="muted-help">Здесь отображаются только сообщения `gptadmin-hub.service`. Перед выдачей Hub маскирует известные форматы токенов и паролей. Для просмотра вызовов клиентов используйте «Запросы».</p>
  </div>;
}
