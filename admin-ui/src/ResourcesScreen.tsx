import { useEffect, useState } from "react";
import { ErrorNotice, PageHeader } from "./ui/Primitives";

type Server = { server_id?: string; status?: string };

async function postJson(path: string, body: unknown): Promise<unknown> {
  const response = await fetch(path, { method: "POST", credentials: "same-origin", headers: { Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const text = await response.text();
  let data: unknown = text;
  try { data = text ? JSON.parse(text) : {}; } catch { /* keep text */ }
  if (!response.ok) throw new Error(typeof data === "object" && data && "detail" in data ? String((data as { detail?: unknown }).detail) : `HTTP ${response.status}`);
  return data;
}

function extractFirstUri(value: unknown): string {
  if (!value || typeof value !== "object") return "";
  const outer = value as { response?: { resources?: Array<{ uri?: string }>; result?: { resources?: Array<{ uri?: string }> } } };
  return outer.response?.resources?.[0]?.uri || outer.response?.result?.resources?.[0]?.uri || "";
}

export default function ResourcesScreen() {
  const [servers, setServers] = useState<Server[]>([]);
  const [target, setTarget] = useState("");
  const [uri, setUri] = useState("");
  const [result, setResult] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    void fetch("/admin/api/overview?limit=1", { credentials: "same-origin", signal: controller.signal, headers: { Accept: "application/json" } })
      .then((response) => response.json())
      .then((data: { servers?: Server[] }) => { const next = Array.isArray(data.servers) ? data.servers : []; setServers(next); setTarget((current) => current || next[0]?.server_id || ""); })
      .catch((caught) => { if ((caught as DOMException).name !== "AbortError") setError(caught instanceof Error ? caught.message : "Не удалось загрузить серверы"); });
    return () => controller.abort();
  }, []);

  const run = async (kind: "list" | "read") => {
    setBusy(true); setError(null);
    try {
      if (!target) throw new Error("Выберите target");
      if (kind === "read" && !uri.trim()) throw new Error("Укажите URI ресурса");
      const data = await postJson(kind === "list" ? "/admin/api/mcp/resources/list" : "/admin/api/mcp/resources/read", kind === "list" ? { target, timeout: 30, background: false } : { target, uri: uri.trim(), timeout: 30, background: false });
      setResult(data);
      if (kind === "list") { const first = extractFirstUri(data); if (first) setUri(first); }
    } catch (caught) { setError(caught instanceof Error ? caught.message : "Операция не выполнена"); }
    finally { setBusy(false); }
  };

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="РАСШИРЕННЫЙ РЕЖИМ" title="Ресурсы" description="Просмотр ресурсов, которые предоставляет выбранный сервис." />
    <ErrorNotice message={error} />
    <section className="card resource-native-card">
      <div className="resource-native-controls"><select value={target} onChange={(event) => setTarget(event.target.value)}>{servers.map((server) => <option value={server.server_id} key={server.server_id}>{server.server_id} ({server.status || "—"})</option>)}</select><button className="button secondary" type="button" disabled={busy || !target} onClick={() => void run("list")}>Показать ресурсы</button><input value={uri} onChange={(event) => setUri(event.target.value)} placeholder="resource://..." /><button className="button primary" type="button" disabled={busy || !uri.trim()} onClick={() => void run("read")}>Открыть ресурс</button></div>
      <pre className="raw-native-box">{result === null ? "—" : JSON.stringify(result, null, 2)}</pre>
    </section>
  </div>;
}
