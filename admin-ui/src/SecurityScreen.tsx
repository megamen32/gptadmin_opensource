import { useCallback, useEffect, useState } from "react";
import { ErrorNotice, PageHeader } from "./ui/Primitives";

type Approval = { approval_id?: string; status?: string; tool?: string; target?: string; actor?: string };
type PresetResponse = { preset?: string; mfa_enrolled?: boolean; updated_at?: string };
type TelemetryResponse = { enabled?: boolean; local_only?: boolean; counters?: Record<string, unknown> };

type SecurityState = { preset: string; mfaEnrolled: boolean; heartbeat: boolean; telemetry: boolean };
const initialState: SecurityState = { preset: "working_default", mfaEnrolled: false, heartbeat: false, telemetry: false };

async function requestJson(path: string, init?: RequestInit): Promise<unknown> {
  const response = await fetch(path, { credentials: "same-origin", ...init, headers: { Accept: "application/json", "Content-Type": "application/json", ...(init?.headers ?? {}) } });
  const text = await response.text();
  let data: unknown = text;
  try { data = text ? JSON.parse(text) : {}; } catch { /* keep text */ }
  if (!response.ok) throw new Error(typeof data === "object" && data && "detail" in data ? String((data as { detail?: unknown }).detail) : text || `HTTP ${response.status}`);
  return data;
}

function decodeBase64Url(value: string): Uint8Array {
  const normalized = value.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - value.length % 4) % 4);
  const raw = atob(normalized);
  return Uint8Array.from(raw, (char) => char.charCodeAt(0));
}
function encodeBase64Url(value: ArrayBuffer): string {
  let raw = "";
  for (const byte of new Uint8Array(value)) raw += String.fromCharCode(byte);
  return btoa(raw).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export default function SecurityScreen() {
  const [state, setState] = useState<SecurityState>(initialState);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [mfaCode, setMfaCode] = useState("");
  const [mfaResult, setMfaResult] = useState<unknown>(null);
  const [passkeyResult, setPasskeyResult] = useState<unknown>(null);
  const [telemetryInfo, setTelemetryInfo] = useState<TelemetryResponse | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    setBusy(true); setError(null);
    try {
      const [presetResponse, envResponse, telemetryResponse, approvalsResponse] = await Promise.all([
        fetch("/admin/api/security/preset", { credentials: "same-origin", signal, headers: { Accept: "application/json" } }),
        fetch("/admin/api/security/env", { credentials: "same-origin", signal, headers: { Accept: "application/json" } }),
        fetch("/admin/api/telemetry", { credentials: "same-origin", signal, headers: { Accept: "application/json" } }),
        fetch("/admin/api/approvals", { credentials: "same-origin", signal, headers: { Accept: "application/json" } }),
      ]);
      if (![presetResponse, envResponse, telemetryResponse, approvalsResponse].every((response) => response.ok)) throw new Error("Один из security endpoints недоступен");
      const preset = await presetResponse.json() as PresetResponse;
      const env = await envResponse.json() as { shellmcp_heartbeat?: boolean };
      const telemetry = await telemetryResponse.json() as TelemetryResponse;
      const approvalData = await approvalsResponse.json() as { approvals?: Approval[] };
      setState({ preset: preset.preset || "working_default", mfaEnrolled: Boolean(preset.mfa_enrolled), heartbeat: Boolean(env.shellmcp_heartbeat), telemetry: Boolean(telemetry.enabled) });
      setTelemetryInfo(telemetry);
      setApprovals(Array.isArray(approvalData.approvals) ? approvalData.approvals : []);
    } catch (caught) {
      if ((caught as DOMException).name !== "AbortError") setError(caught instanceof Error ? caught.message : "Не удалось загрузить security state");
    } finally { if (!signal?.aborted) setBusy(false); }
  }, []);

  useEffect(() => { const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load]);

  const reauth = async (): Promise<boolean> => {
    const password = window.prompt("Введите admin-пароль для подтверждения изменения безопасности:");
    if (password === null) return false;
    try {
      await requestJson("/admin/api/security/reauth", { method: "POST", body: JSON.stringify({ password, code: mfaCode.trim() }) });
      return true;
    } catch (caught) { setError(caught instanceof Error ? caught.message : "Повторная авторизация не выполнена"); return false; }
  };

  const savePreset = async () => {
    if (!await reauth()) return;
    setBusy(true); setError(null);
    try { await requestJson("/admin/api/security/preset", { method: "PUT", body: JSON.stringify({ preset: state.preset }) }); await load(); }
    catch (caught) { setError(caught instanceof Error ? caught.message : "Профиль не сохранён"); }
    finally { setBusy(false); }
  };

  const enrollTotp = async () => {
    setBusy(true); setError(null);
    try { setMfaResult(await requestJson("/admin/api/security/mfa/totp/enroll", { method: "POST", body: "{}" })); }
    catch (caught) { setError(caught instanceof Error ? caught.message : "ПОДТВЕРЖДЕНИЕ ВХОДА enrollment не выполнен"); }
    finally { setBusy(false); }
  };
  const verifyTotp = async () => {
    if (!mfaCode.trim()) { setError("Введите ПОДТВЕРЖДЕНИЕ ВХОДА-код"); return; }
    setBusy(true); setError(null);
    try { setMfaResult(await requestJson("/admin/api/security/mfa/totp/verify", { method: "POST", body: JSON.stringify({ code: mfaCode.trim() }) })); await load(); }
    catch (caught) { setError(caught instanceof Error ? caught.message : "ПОДТВЕРЖДЕНИЕ ВХОДА-код не подтверждён"); }
    finally { setBusy(false); }
  };

  const enrollPasskey = async () => {
    setError(null);
    if (!window.PublicKeyCredential || !navigator.credentials?.create) { setError("WebAuthn недоступен в этом браузере"); return; }
    setBusy(true);
    try {
      const begin = await requestJson("/admin/api/security/mfa/webauthn/register/begin", { method: "POST", body: "{}" }) as { publicKey?: PublicKeyCredentialCreationOptions & { challenge: string; user?: { id: string }; excludeCredentials?: Array<PublicKeyCredentialDescriptor & { id: string }> } };
      if (!begin.publicKey) throw new Error("Hub не вернул параметры WebAuthn");
      const publicKey = { ...begin.publicKey, challenge: decodeBase64Url(begin.publicKey.challenge), user: begin.publicKey.user ? { ...begin.publicKey.user, id: decodeBase64Url(begin.publicKey.user.id) } : begin.publicKey.user, excludeCredentials: begin.publicKey.excludeCredentials?.map((item) => ({ ...item, id: decodeBase64Url(String(item.id)) })) } as PublicKeyCredentialCreationOptions;
      const credential = await navigator.credentials.create({ publicKey }) as PublicKeyCredential | null;
      if (!credential) throw new Error("Регистрация passkey отменена");
      const response = credential.response as AuthenticatorAttestationResponse;
      const result = await requestJson("/admin/api/security/mfa/webauthn/register/finish", { method: "POST", body: JSON.stringify({ id: credential.id, rawId: encodeBase64Url(credential.rawId), response: { clientDataJSON: encodeBase64Url(response.clientDataJSON), attestationObject: encodeBase64Url(response.attestationObject) }, type: credential.type }) });
      setPasskeyResult(result);
      await load();
    } catch (caught) { setError(caught instanceof Error ? caught.message : "Passkey не зарегистрирован"); }
    finally { setBusy(false); }
  };

  const toggleHeartbeat = async (enabled: boolean) => {
    if (!await reauth()) return;
    setBusy(true); setError(null);
    try { await requestJson("/admin/api/security/heartbeat", { method: "POST", body: JSON.stringify({ enabled }) }); setState((current) => ({ ...current, heartbeat: enabled })); }
    catch (caught) { setError(caught instanceof Error ? caught.message : "Heartbeat не изменён"); }
    finally { setBusy(false); }
  };
  const toggleTelemetry = async (enabled: boolean) => {
    setBusy(true); setError(null);
    try { const result = await requestJson("/admin/api/telemetry", { method: "PUT", body: JSON.stringify({ enabled }) }) as TelemetryResponse; setTelemetryInfo(result); setState((current) => ({ ...current, telemetry: enabled })); }
    catch (caught) { setError(caught instanceof Error ? caught.message : "Telemetry не изменена"); }
    finally { setBusy(false); }
  };
  const decide = async (id: string, action: "approve" | "reject") => {
    setBusy(true); setError(null);
    try { await requestJson(`/admin/api/approvals/${encodeURIComponent(id)}`, { method: "POST", body: JSON.stringify({ action }) }); await load(); }
    catch (caught) { setError(caught instanceof Error ? caught.message : "Approval не обработан"); }
    finally { setBusy(false); }
  };
  const rotateOAuth = async () => {
    if (!window.confirm("Обновить внутреннюю OAuth-конфигурацию? Подключения MCP потребуется проверить заново.")) return;
    if (!await reauth()) return;
    setBusy(true); setError(null);
    try { await requestJson("/admin/api/auth/rotate-oauth", { method: "POST" }); await load(); }
    catch (caught) { setError(caught instanceof Error ? caught.message : "OAuth-сессия не обновлена"); }
    finally { setBusy(false); }
  };

  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="РАСШИРЕННЫЕ НАСТРОЙКИ" title="Безопасность" description="Редкие параметры защиты. Обычные настройки доступа находятся в разделе «Доступ»." actions={<button className="button secondary" type="button" disabled={busy} onClick={() => void load()}>Обновить</button>} />
    <ErrorNotice message={error} />
    <section className="security-native-grid">
      <article className="card security-section"><div className="card-heading"><div><p className="section-kicker">ПРОФИЛЬ</p><h2>Профиль безопасности</h2></div><span className={`small-pill ${state.mfaEnrolled ? "" : "is-disabled"}`}>Доп. защита {state.mfaEnrolled ? "включена" : "выключена"}</span></div><div className="security-body"><select value={state.preset} onChange={(event) => setState({ ...state, preset: event.target.value })}><option value="working_default">Обычная защита</option><option value="private_access">Только доверенный доступ</option><option value="locked_down">Максимальная защита</option></select><button className="button primary" type="button" disabled={busy} onClick={() => void savePreset()}>Сохранить профиль</button><p className="muted-help">Максимальная защита требует настроенного дополнительного подтверждения входа.</p></div></article>
      <article className="card security-section"><div className="card-heading"><div><p className="section-kicker">ПОДТВЕРЖДЕНИЕ ВХОДА</p><h2>Администратор</h2></div></div><div className="security-body"><div className="security-inline"><button className="button secondary" type="button" disabled={busy} onClick={() => void enrollTotp()}>Настроить код из приложения</button><input inputMode="numeric" value={mfaCode} onChange={(event) => setMfaCode(event.target.value)} placeholder="6-значный код" /><button className="button primary" type="button" disabled={busy} onClick={() => void verifyTotp()}>Подтвердить</button></div><button className="button secondary" type="button" disabled={busy} onClick={() => void enrollPasskey()}>Добавить ключ входа</button>{mfaResult !== null && <pre className="raw-native-box compact-result">{JSON.stringify(mfaResult, null, 2)}</pre>}{passkeyResult !== null && <pre className="raw-native-box compact-result">{JSON.stringify(passkeyResult, null, 2)}</pre>}</div></article>
      <article className="card security-section"><div className="card-heading"><div><p className="section-kicker">ДИАГНОСТИКА</p><h2>Диагностика</h2></div></div><div className="security-body"><label className="switch-line"><input type="checkbox" checked={state.heartbeat} onChange={(event) => void toggleHeartbeat(event.target.checked)} /><span><strong>Дополнительная проверка связи</strong><small>Нужна только для диагностики</small></span></label><label className="switch-line"><input type="checkbox" checked={state.telemetry} onChange={(event) => void toggleTelemetry(event.target.checked)} /><span><strong>Локальная статистика</strong><small>Данные остаются на этом сервере</small></span></label>{telemetryInfo && <pre className="raw-native-box compact-result">{JSON.stringify({ local_only: telemetryInfo.local_only, counters: telemetryInfo.counters ?? {} }, null, 2)}</pre>}</div></article>
      <article className="card security-section"><details className="danger-zone-details"><summary>Опасные системные действия</summary><div className="security-body"><p className="muted-help">Используйте только при проблемах с внутренней авторизацией. После этого подключения может потребоваться настроить заново.</p><button className="button danger" type="button" disabled={busy} onClick={() => void rotateOAuth()}>Обновить внутренние ключи OAuth</button></div></details></article>
      <article className="card security-section approvals-section"><div className="card-heading"><div><p className="section-kicker">ПОДТВЕРЖДЕНИЯ</p><h2>Ожидающие подтверждения</h2></div><span className="count-pill">{approvals.filter((item) => item.status === "pending").length}</span></div><div className="approval-list">{approvals.length === 0 ? <div className="compact-empty"><strong>Запросов нет</strong><span>Ничего не ожидает подтверждения.</span></div> : approvals.map((approval) => <article className="approval-row" key={approval.approval_id}><div><strong>{approval.tool || "operation"}</strong><small>{approval.target || "—"} · {approval.actor || "—"} · {approval.status || "unknown"}</small></div>{approval.status === "pending" && <div className="row-actions"><button className="text-button" type="button" disabled={busy} onClick={() => void decide(approval.approval_id || "", "approve")}>Разрешить</button><button className="text-button danger-text" type="button" disabled={busy} onClick={() => void decide(approval.approval_id || "", "reject")}>Отклонить</button></div>}</article>)}</div></article>
    </section>
  </div>;
}
