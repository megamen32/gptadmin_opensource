import { useEffect, useRef, useState } from "react";

type Operation = { operation_id: string; actor: string; action: string; target: string; status: string; started_at: string; http_status?: number };
function description(operation: Operation): string {
  if (operation.target.includes("access-profiles")) return "Сохранение профиля";
  if (operation.target.includes("client-bindings")) return operation.action === "DELETE" ? "Снятие профиля" : "Назначение профиля";
  if (operation.target.includes("issue-token")) return "Создание подключения";
  if (operation.target.includes("/rotate")) return "Ротация подключения";
  if (operation.target.includes("/clients/")) return "Отзыв подключения";
  return operation.action;
}

export default function AccessOperations() {
  const [operations, setOperations] = useState<Operation[]>([]);
  const [offset, setOffset] = useState(0);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const request = useRef(0);
  useEffect(() => {
    const current = ++request.current;
    const controller = new AbortController();
    setLoading(true); setError("");
    void fetch(`/admin/api/operations?limit=50&offset=${offset}`, { credentials: "same-origin", signal: controller.signal, headers: { Accept: "application/json" } })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Не удалось загрузить историю: HTTP ${response.status}`);
        const data = await response.json() as { operations?: Operation[]; total?: number };
        if (!Array.isArray(data.operations) || typeof data.total !== "number") throw new Error("Некорректный ответ истории операций");
        if (request.current === current) { setOperations(data.operations); setTotal(data.total); }
      }).catch((reason: unknown) => { if (!controller.signal.aborted && request.current === current) setError(reason instanceof Error ? reason.message : "Ошибка загрузки"); })
      .finally(() => { if (!controller.signal.aborted && request.current === current) setLoading(false); });
    return () => controller.abort();
  }, [offset, revision]);
  return <><header className="topbar"><div><span className="eyebrow">ИСТОРИЯ ИЗМЕНЕНИЙ</span><h1>Операции доступа</h1></div><button className="button secondary" onClick={() => setRevision((value) => value + 1)}>Обновить</button></header>
    <div className="content-wrap"><p className="lede">Изменения профилей и подключений через ИИ и вебморду. История сохраняется на сервере. Выполнение команд и их результаты находятся в разделе «Задачи».</p>
      {error && <p role="alert">{error}</p>}
      {loading && <p role="status">Загрузка операций…</p>}
      {!loading && !error && operations.length === 0 && <p>Операций доступа пока нет.</p>}
      {operations.length > 0 && <section className="card" style={{ overflowX: "auto", padding: 20 }}><table><thead><tr><th>Когда</th><th>Действие</th><th>Объект</th><th>Кто</th><th>Результат</th></tr></thead><tbody>{operations.map((operation) => <tr key={operation.operation_id}><td>{new Date(operation.started_at).toLocaleString("ru-RU")}</td><td>{description(operation)}</td><td><code>{operation.target.split("/").pop()}</code><details><summary>ID операции</summary><code>{operation.operation_id}</code></details></td><td>{operation.actor}</td><td>{operation.status === "completed" ? "Выполнено" : operation.status === "failed" ? `Ошибка ${operation.http_status ?? ""}` : "Не завершена: требуется проверка"}</td></tr>)}</tbody></table></section>}
      <div className="button-row" style={{ marginTop: 16 }}><button className="button secondary" disabled={offset === 0 || loading} onClick={() => setOffset(Math.max(0, offset - 50))}>Новее</button><span>{total} операций</span><button className="button secondary" disabled={offset + 50 >= total || loading} onClick={() => setOffset(offset + 50)}>Старее</button></div>
    </div></>;
}
