import { useCallback, useEffect, useState } from "react";
import { ErrorNotice, PageHeader } from "./ui/Primitives";

type Server = { server_id?: string; status?: string };
type NodeConfig = { server_id: string; enabled: boolean; rank: number; hub_url: string; local_hub_port?: number };
type Config = { enabled?: boolean; primary_public_url?: string; fail_count_base?: number; deterministic_rank_backoff?: boolean; nodes?: NodeConfig[] };

type FailoverResponse = { config?: Config; state?: unknown };

export default function FailoverScreen() {
  const [servers, setServers] = useState<Server[]>([]);
  const [enabled, setEnabled] = useState(false);
  const [primary, setPrimary] = useState("");
  const [base, setBase] = useState(3);
  const [nodes, setNodes] = useState<Record<string, NodeConfig>>({});
  const [state, setState] = useState<unknown>(null);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    setBusy(true); setError(null);
    try {
      const [overviewResponse, failoverResponse] = await Promise.all([
        fetch("/admin/api/overview?limit=1", { credentials: "same-origin", signal, headers: { Accept: "application/json" } }),
        fetch("/admin/api/failover", { credentials: "same-origin", signal, headers: { Accept: "application/json" } }),
      ]);
      if (!overviewResponse.ok || !failoverResponse.ok) throw new Error(`Hub вернул HTTP ${!overviewResponse.ok ? overviewResponse.status : failoverResponse.status}`);
      const overview = await overviewResponse.json() as { servers?: Server[] };
      const failover = await failoverResponse.json() as FailoverResponse;
      const shells = (overview.servers ?? []).filter((server) => String(server.server_id || "").startsWith("shell:"));
      const config = failover.config ?? {};
      const existing = Object.fromEntries((config.nodes ?? []).map((node) => [node.server_id, node]));
      setServers(shells);
      setEnabled(Boolean(config.enabled));
      setPrimary(config.primary_public_url || "");
      setBase(config.fail_count_base || 3);
      setNodes(Object.fromEntries(shells.map((server, index) => {
        const id = server.server_id || `shell-${index}`;
        return [id, existing[id] ?? { server_id: id, enabled: false, rank: index + 1, hub_url: "", local_hub_port: 9001 }];
      })));
      setState(failover.state ?? failover);
    } catch (caught) {
      if ((caught as DOMException).name !== "AbortError") setError(caught instanceof Error ? caught.message : "Не удалось загрузить failover");
    } finally { if (!signal?.aborted) setBusy(false); }
  }, []);

  useEffect(() => { const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load]);

  const updateNode = (id: string, patch: Partial<NodeConfig>) => setNodes((current) => ({ ...current, [id]: { ...current[id], server_id: id, ...patch } }));

  const save = async () => {
    setBusy(true); setError(null);
    try {
      const body = { enabled, primary_public_url: primary.trim(), fail_count_base: base || 3, deterministic_rank_backoff: true, nodes: Object.values(nodes).filter((node) => node.enabled).map((node) => ({ ...node, local_hub_port: node.local_hub_port || 9001 })) };
      const response = await fetch("/admin/api/failover", { method: "POST", credentials: "same-origin", headers: { Accept: "application/json", "Content-Type": "application/json" }, body: JSON.stringify(body) });
      const text = await response.text();
      if (!response.ok) throw new Error(text || `HTTP ${response.status}`);
      await load();
    } catch (caught) { setError(caught instanceof Error ? caught.message : "Failover не сохранён"); }
    finally { setBusy(false); }
  };

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="РАСШИРЕННЫЙ РЕЖИМ" title="Резервирование" description="Автоматическое переключение на резервный сервер при сбое." actions={<><button className="button secondary" type="button" disabled={busy} onClick={() => void load()}>Обновить</button><button className="button primary" type="button" disabled={busy} onClick={() => void save()}>Сохранить</button></>} />
    <ErrorNotice message={error} />
    <section className="card failover-main-card">
      <div className="failover-main-settings"><label className="switch-line"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>Автоматическое резервирование</strong><small>Разрешить переход на резервный сервер при сбое</small></span></label><label><span>Основной адрес системы</span><input value={primary} onChange={(event) => setPrimary(event.target.value)} placeholder="https://..." /></label><label><span>Ошибок до переключения</span><input type="number" min={1} value={base} onChange={(event) => setBase(Number(event.target.value) || 3)} /></label></div>
      <div className="failover-node-list">{servers.length === 0 ? <div className="compact-empty"><strong>Резервных серверов нет</strong><span>Настройка станет доступна после подключения дополнительного сервера.</span></div> : servers.map((server) => { const id = server.server_id || ""; const node = nodes[id] ?? { server_id: id, enabled: false, rank: 1, hub_url: "" }; return <article className="failover-node-row" key={id}><label className="switch-line"><input type="checkbox" checked={node.enabled} onChange={(event) => updateNode(id, { enabled: event.target.checked })} /><span><strong>{id}</strong><small>{server.status || "unknown"}</small></span></label><label><span>Приоритет</span><input type="number" min={1} value={node.rank} onChange={(event) => updateNode(id, { rank: Number(event.target.value) || 1 })} /></label><label className="hub-url-field"><span>Адрес резервного сервера</span><input value={node.hub_url} onChange={(event) => updateNode(id, { hub_url: event.target.value })} placeholder="http://192.168.2.x:9001" /></label></article>; })}</div>
    </section>
    <details className="card operation-result"><summary>Техническое состояние</summary><pre className="raw-native-box">{state === null ? "—" : JSON.stringify(state, null, 2)}</pre></details>
  </div>;
}
