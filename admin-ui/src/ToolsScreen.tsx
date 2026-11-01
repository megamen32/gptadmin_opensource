import { useEffect, useMemo, useState } from "react";
import { ErrorNotice, PageHeader } from "./ui/Primitives";
import { getTaskReceipt } from "./api";

type ToolDef = { name?: string; description?: string };
type Server = { server_id?: string; status?: string };

type JsonValue = unknown;

async function jsonRequest(path: string, init?: RequestInit): Promise<JsonValue> {
  const response = await fetch(path, { credentials: "same-origin", ...init, headers: { Accept: "application/json", "Content-Type": "application/json", ...(init?.headers ?? {}) } });
  const text = await response.text();
  let data: unknown = text;
  try { data = text ? JSON.parse(text) : {}; } catch { /* keep text */ }
  if (!response.ok) throw new Error(typeof data === "object" && data && "detail" in data ? String((data as { detail?: unknown }).detail) : `HTTP ${response.status}`);
  return data;
}

export default function ToolsScreen() {
  const [servers, setServers] = useState<Server[]>([]);
  const [target, setTarget] = useState("");
  const [tools, setTools] = useState<ToolDef[]>([]);
  const [toolName, setToolName] = useState("");
  const [args, setArgs] = useState("{}");
  const [timeout, setTimeoutValue] = useState(30);
  const [background, setBackground] = useState(false);
  const [jobId, setJobId] = useState("");
  const [instructionContext, setInstructionContext] = useState("");
  const [result, setResult] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    void fetch("/admin/api/overview?limit=1", { credentials: "same-origin", signal: controller.signal, headers: { Accept: "application/json" } })
      .then((response) => response.json())
      .then((data: { servers?: Server[] }) => {
        const next = Array.isArray(data.servers) ? data.servers : [];
        setServers(next);
        setTarget((current) => current || next[0]?.server_id || "");
      })
      .catch((caught) => { if ((caught as DOMException).name !== "AbortError") setError(caught instanceof Error ? caught.message : "Не удалось загрузить серверы"); });
    return () => controller.abort();
  }, []);

  const selectedTool = useMemo(() => tools.find((tool) => tool.name === toolName), [tools, toolName]);
  const run = async (action: "list" | "call" | "job", approvedContext = "") => {
    setBusy(true); setError(null);
    try {
      if (action === "list") {
        const data = await jsonRequest("/admin/api/mcp/tool", {
          method: "POST", body: JSON.stringify({ target, method: "tools/list" }),
        }) as { result?: { tools?: ToolDef[] } };
        const next = Array.isArray(data.result?.tools) ? data.result.tools : [];
        setTools(next);
        setToolName(next[0]?.name || "");
        setResult(data.result ?? data);
      } else if (action === "call") {
        let parsed: unknown;
        try { parsed = JSON.parse(args || "{}"); } catch (caught) { throw new Error(`Некорректный JSON: ${caught instanceof Error ? caught.message : "ошибка"}`, { cause: caught }); }
        if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Аргументы должны быть JSON-объектом");
        setJobId("");
        setInstructionContext("");
        const data = await jsonRequest("/admin/api/mcp/tool", {
          method: "POST",
          body: JSON.stringify({ target, method: "tools/call", name: toolName, arguments: parsed, ...(approvedContext ? { context: approvedContext } : {}) }),
        }) as { result?: Record<string, unknown> };
        const outcome = data.result;
        if (!outcome || typeof outcome !== "object") throw new Error("Нативный MCP не вернул результат");
        setResult(outcome);
        if (outcome.resultType === "task" && typeof outcome.taskId === "string" && outcome.taskId) {
          const taskId = outcome.taskId;
          setJobId(taskId);
          if (!background) {
            const deadline = Date.now() + timeout * 1000;
            let receipt: Record<string, unknown> = outcome;
            while (Date.now() < deadline) {
              const pollMs = Math.min(3000, Math.max(250, typeof receipt.pollIntervalMs === "number" ? receipt.pollIntervalMs : 1000));
              await new Promise<void>((resolve) => setTimeout(resolve, pollMs));
              receipt = await getTaskReceipt(taskId);
              setResult(receipt);
              if (receipt.status !== "working") break;
            }
          }
        } else {
          const structured = outcome.structuredContent;
          if (structured && typeof structured === "object") {
            const payload = structured as Record<string, unknown>;
            const failure = payload.error;
            if (payload.executed === false && failure && typeof failure === "object" &&
                (failure as Record<string, unknown>).code === "instructions_required" &&
                typeof payload.instructions === "string" && payload.instructions.trim() &&
                typeof payload.context === "string" && payload.context) {
              setInstructionContext(payload.context);
            }
          }
        }
      } else {
        if (!jobId.trim()) throw new Error("Укажите taskId");
        setResult(await getTaskReceipt(jobId.trim()));
      }
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Операция не выполнена");
    } finally { setBusy(false); }
  };

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="РАСШИРЕННЫЙ РЕЖИМ" title="Выполнить вручную" description="Вручную запустить действие на выбранном сервере." />
    <ErrorNotice message={error} />
    <section className="card tool-native-card">
      <div className="tool-native-controls">
        <label><span>Сервер</span><select value={target} onChange={(event) => setTarget(event.target.value)}>{servers.map((server) => <option value={server.server_id} key={server.server_id}>{server.server_id} ({server.status || "—"})</option>)}</select></label>
        <label><span>Ждать результат до, сек.</span><input type="number" min={1} max={3600} value={timeout} onChange={(event) => setTimeoutValue(Number(event.target.value) || 30)} /></label>
        <label className="checkbox-line"><input type="checkbox" checked={background} onChange={(event) => setBackground(event.target.checked)} /> Не ждать завершения задачи</label>
        <button className="button secondary" type="button" disabled={busy || !target} onClick={() => void run("list")}>Получить список действий</button>
      </div>
      <div className="tool-native-grid">
        <div className="tool-editor-pane">
          <label><span>Действие</span><select value={toolName} onChange={(event) => setToolName(event.target.value)}><option value="">Выберите tool</option>{tools.map((tool) => <option value={tool.name} key={tool.name}>{tool.name}</option>)}</select></label>
          {selectedTool?.description && <p className="muted-help">{selectedTool.description}</p>}
          <label><span>Параметры действия (JSON)</span><textarea value={args} onChange={(event) => setArgs(event.target.value)} rows={12} /></label>
          <div className="route-actions"><button className="button secondary" type="button" onClick={() => { try { setArgs(JSON.stringify(JSON.parse(args || "{}"), null, 2)); } catch { setError("Некорректный JSON"); } }}>Форматировать</button><button className="button primary" type="button" disabled={busy || !toolName} onClick={() => void run("call")}>Вызвать</button></div>
          {instructionContext && <button className="button secondary" type="button" disabled={busy} onClick={() => void run("call", instructionContext)}>Я прочитал инструкции, выполнить</button>}
        </div>
        <div className="tool-result-pane">
          <div className="job-inline-control"><input value={jobId} onChange={(event) => setJobId(event.target.value)} placeholder="MCP taskId" /><button className="button secondary" type="button" disabled={busy || !jobId.trim()} onClick={() => void run("job")}>Показать задачу</button></div>
          <pre className="raw-native-box tool-result-box">{result === null ? "—" : JSON.stringify(result, null, 2)}</pre>
        </div>
      </div>
    </section>
  </div>;
}
