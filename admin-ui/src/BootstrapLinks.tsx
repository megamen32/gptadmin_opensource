import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import {
  ApiError,
  createBootstrapLink,
  getAccessProfiles,
  getBootstrapLinks,
  revokeBootstrapLink,
  type AccessProfile,
  type BootstrapLink,
  type CreatedBootstrapLink,
} from "./api";
import { formatTimestamp } from "./ui/format";
import "./BootstrapLinks.css";

const statusLabels: Record<BootstrapLink["status"], string> = {
  active: "Действует",
  expired: "Срок истёк",
  revoked: "Отозвана",
};

function messageFor(error: unknown): string {
  if (error instanceof ApiError && error.status === 401) return "Сессия завершена. Войдите в GPTAdmin снова.";
  if (error instanceof ApiError && error.status === 403) return "Для управления ссылками нужен доступ администратора.";
  if (error instanceof TypeError) return "Не удалось связаться с GPTAdmin.";
  return error instanceof Error ? error.message : "Не удалось выполнить запрос.";
}

function effectiveStatus(link: BootstrapLink): BootstrapLink["status"] {
  if (link.status === "revoked" || link.revoked_at > 0) return "revoked";
  return link.expires_at <= Date.now() / 1000 ? "expired" : link.status;
}

export default function BootstrapLinks() {
  const [items, setItems] = useState<BootstrapLink[]>([]);
  const [profiles, setProfiles] = useState<AccessProfile[]>([]);
  const [label, setLabel] = useState("");
  const [profileId, setProfileId] = useState("");
  const [ttlDays, setTtlDays] = useState("7");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [creationUnknown, setCreationUnknown] = useState(false);
  const [created, setCreated] = useState<CreatedBootstrapLink | null>(null);
  const request = useRef(0);
  const promptRef = useRef<HTMLTextAreaElement>(null);
  const urlRef = useRef<HTMLInputElement>(null);

  const load = useCallback(async () => {
    const current = ++request.current;
    setLoading(true);
    setError("");
    try {
      const [links, availableProfiles] = await Promise.all([getBootstrapLinks(), getAccessProfiles()]);
      if (request.current !== current) return;
      setItems(links);
      setProfiles(availableProfiles);
      setProfileId((value) => availableProfiles.some((profile) => profile.id === value) ? value : "");
      setCreated((value) => value && links.some((link) => link.id === value.link.id && effectiveStatus(link) === "active") ? value : null);
      setCreationUnknown(false);
    } catch (reason) {
      if (request.current === current) setError(messageFor(reason));
    } finally {
      if (request.current === current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    return () => { request.current += 1; };
  }, [load]);

  const days = Number(ttlDays);
  const selectedProfile = profiles.find((profile) => profile.id === profileId);
  const canCreate = label.trim().length > 0 && !!selectedProfile && Number.isInteger(days) && days >= 1 && days <= 3650;

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canCreate || busy || loading || creationUnknown) return;
    setBusy("create");
    setError("");
    setNotice("");
    try {
      const result = await createBootstrapLink({ label: label.trim(), profile_id: profileId, ttl_days: days });
      setCreated(result);
      setItems((current) => [result.link, ...current.filter((link) => link.id !== result.link.id)]);
      setNotice("Ссылка создана. Скопируйте готовое сообщение для внешнего агента.");
    } catch (reason) {
      const uncertain = reason instanceof TypeError || (reason instanceof ApiError && reason.status >= 500);
      setCreationUnknown(uncertain);
      setError(uncertain
        ? "Ответ о создании не получен. Обновите список, чтобы проверить результат перед новой попыткой."
        : messageFor(reason));
    } finally {
      setBusy(null);
    }
  }

  async function copy(kind: "prompt" | "url") {
    if (!created) return;
    setError("");
    setNotice("");
    const value = kind === "prompt" ? created.prompt : created.url;
    try {
      await navigator.clipboard.writeText(value);
      setNotice(kind === "prompt" ? "Сообщение скопировано. Вставьте его в чат с внешним агентом." : "Ссылка скопирована.");
    } catch {
      const element = kind === "prompt" ? promptRef.current : urlRef.current;
      element?.focus();
      element?.select();
      setError("Браузер не разрешил копирование. Текст выделен, скопируйте его вручную.");
    }
  }

  async function revoke(link: BootstrapLink) {
    if (busy || loading) return;
    setBusy(link.id);
    setError("");
    setNotice("");
    try {
      await revokeBootstrapLink(link.id);
      setItems((current) => current.map((item) => item.id === link.id ? { ...item, status: "revoked" } : item));
      setCreated((current) => current?.link.id === link.id ? null : current);
      setNotice(`Доступ «${link.label}» отозван. Ссылка и скачанный клиент больше не работают.`);
    } catch (reason) {
      setError(messageFor(reason));
    } finally {
      setBusy(null);
    }
  }

  return <>
    <header className="topbar">
      <div><span className="eyebrow">ПОДКЛЮЧЕНИЕ ПО ССЫЛКЕ</span><h1>Внешние агенты</h1></div>
      <button className="button secondary topbar-action" type="button" disabled={loading || busy !== null} onClick={() => void load()}>
        {loading ? "Загружаем…" : "Обновить"}
      </button>
    </header>
    <div className="content-wrap bootstrap-links">
      <p className="lede bootstrap-intro">Отправьте агенту одну ссылку. По ней он получит инструкции, готовый MCP-клиент и доступ в пределах выбранного профиля.</p>

      {error && <div className="state-panel card state-error" role="alert">{error}</div>}
      {notice && <div className="bootstrap-notice" role="status">{notice}</div>}

      <section className="card bootstrap-card" aria-labelledby="bootstrap-create-title">
        <div className="card-heading"><div><p className="section-kicker">НОВОЕ ПОДКЛЮЧЕНИЕ</p><h2 id="bootstrap-create-title">Создать временную ссылку</h2></div></div>
        <form onSubmit={(event) => void create(event)}>
          <fieldset className="bootstrap-fields" disabled={loading || busy !== null}>
            <label className="bootstrap-field" htmlFor="bootstrap-label">
              <span>Название подключения</span>
              <input id="bootstrap-label" value={label} onChange={(event) => setLabel(event.target.value)} placeholder="Например, Z.ai для GPTAdmin" maxLength={120} required autoComplete="off" />
            </label>
            <label className="bootstrap-field" htmlFor="bootstrap-profile">
              <span>Профиль доступа</span>
              <select id="bootstrap-profile" value={profileId} onChange={(event) => setProfileId(event.target.value)} required>
                <option value="">{loading ? "Загружаем профили…" : "Выберите профиль"}</option>
                {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name} · {profile.access_mode === "readonly" ? "только чтение" : "чтение и изменения"}</option>)}
              </select>
            </label>
            <label className="bootstrap-field bootstrap-ttl" htmlFor="bootstrap-ttl">
              <span>Срок действия, дней</span>
              <input id="bootstrap-ttl" type="number" min="1" max="3650" step="1" value={ttlDays} onChange={(event) => setTtlDays(event.target.value)} required />
            </label>
          </fieldset>
          {selectedProfile && <p className="bootstrap-hint">Профиль «{selectedProfile.name}» определяет доступные серверы, инструменты и инструкции.</p>}
          {!loading && profiles.length === 0 && !error && <p className="bootstrap-hint">Сначала <a href="#profiles">создайте профиль доступа</a>, чтобы выбрать права внешнего агента.</p>}
          <div className="bootstrap-form-footer">
            <p className="bootstrap-hint">Срок по умолчанию 7 дней. Его можно изменить. Доступ можно отозвать в любой момент.</p>
            <button className="button primary" type="submit" disabled={!canCreate || loading || busy !== null || creationUnknown}>{busy === "create" ? "Создаём…" : "Создать ссылку"}</button>
          </div>
        </form>
      </section>

      {created && effectiveStatus(created.link) === "active" && <section className="card bootstrap-card bootstrap-issued" aria-labelledby="bootstrap-issued-title">
        <div className="card-heading">
          <div><p className="section-kicker">ГОТОВО К ОТПРАВКЕ</p><h2 id="bootstrap-issued-title">Сообщение для агента</h2></div>
          <span className="bootstrap-expiry">До {formatTimestamp(created.link.expires_at)}</span>
        </div>
        <label className="bootstrap-field" htmlFor="bootstrap-prompt">
          <span>Скопируйте сообщение и допишите свою задачу</span>
          <textarea id="bootstrap-prompt" ref={promptRef} value={created.prompt} readOnly rows={6} spellCheck={false} />
        </label>
        <div className="bootstrap-copy-row">
          <button className="button primary" type="button" onClick={() => void copy("prompt")}>Копировать сообщение</button>
          <button className="button secondary" type="button" onClick={() => void copy("url")}>Копировать ссылку</button>
        </div>
        <label className="bootstrap-field bootstrap-url" htmlFor="bootstrap-url">
          <span>Ссылка на инструкции и клиент</span>
          <input id="bootstrap-url" ref={urlRef} value={created.url} readOnly spellCheck={false} />
        </label>
        <p className="bootstrap-hint">Сохраните ссылку сейчас: она показывается только после создания. По ней можно получить доступ с выбранными правами.</p>
      </section>}

      <section className="card bootstrap-card" aria-labelledby="bootstrap-list-title">
        <div className="card-heading">
          <div><p className="section-kicker">ВЫДАННЫЕ ПОДКЛЮЧЕНИЯ</p><h2 id="bootstrap-list-title">Временные ссылки</h2></div>
          <span className="bootstrap-count">{items.length}</span>
        </div>
        {loading && <p className="bootstrap-hint" role="status">Загрузка подключений…</p>}
        {!loading && items.length === 0 && !error && <p className="bootstrap-empty">Выданных ссылок пока нет. Создайте первую с помощью формы выше.</p>}
        {items.length > 0 && <div className="bootstrap-table-wrap"><table className="bootstrap-table">
          <thead><tr><th scope="col">Подключение</th><th scope="col">Профиль</th><th scope="col">Действует до</th><th scope="col">Состояние</th><th scope="col"><span className="bootstrap-visually-hidden">Действия</span></th></tr></thead>
          <tbody>{items.map((link) => {
            const status = effectiveStatus(link);
            const profile = profiles.find((value) => value.id === link.profile_id);
            return <tr key={link.id}>
              <td><strong>{link.label}</strong><span className="bootstrap-created-at">Создана {formatTimestamp(link.created_at)}</span></td>
              <td>{profile?.name || link.profile_id}</td>
              <td>{formatTimestamp(link.expires_at)}</td>
              <td><span className={`bootstrap-status bootstrap-status-${status}`}>{statusLabels[status]}</span></td>
              <td>{status === "active" && <button className="button danger" type="button" disabled={busy !== null || loading} onClick={() => void revoke(link)} aria-label={`Отозвать доступ «${link.label}»`}>{busy === link.id ? "Отзываем…" : "Отозвать"}</button>}</td>
            </tr>;
          })}</tbody>
        </table></div>}
      </section>
    </div>
  </>;
}
