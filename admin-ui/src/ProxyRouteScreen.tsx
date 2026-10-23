import { useCallback, useEffect, useState } from "react";
import type { ProxyRouteAction, ProxyRouteProfile, ProxyRouteScreenProps, ProxyRouteState } from "./api";
import { ErrorNotice, PageHeader, StatusBadge } from "./ui/Primitives";

function serviceLabel(value: ProxyRouteState["runtime"]["service_state"]): string {
  switch (value) {
    case "active": return "Работает";
    case "inactive": return "Остановлен";
    case "failed": return "Ошибка";
    default: return "Неизвестно";
  }
}

function listenerLabel(value: ProxyRouteState["runtime"]["listener_state"]): string {
  switch (value) {
    case "listening": return "Слушает";
    case "closed": return "Закрыт";
    default: return "Неизвестно";
  }
}

function pathLabel(value: boolean | null): string {
  if (value === true) return "Путь через сервер 44 подтверждён";
  if (value === false) return "Путь через сервер 44 не подтверждён";
  return "Путь через сервер 44 неизвестен";
}

function formatTime(value: string | null): string {
  if (!value) return "Проверка ещё не выполнялась";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export default function ProxyRouteScreen({ load, save, action }: ProxyRouteScreenProps) {
  const [state, setState] = useState<ProxyRouteState | null>(null);
  const [desiredState, setDesiredState] = useState<ProxyRouteProfile["desired_state"]>("disabled");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      const next = await load();
      setState(next);
      setDesiredState(next.profile.desired_state);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Не удалось загрузить настройку прокси.");
    } finally {
      setBusy(false);
    }
  }, [load]);

  useEffect(() => { void refresh(); }, [refresh]);

  const runAction = async (operation: ProxyRouteAction) => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const next = await action(operation);
      setState(next);
      setDesiredState(next.profile.desired_state);
      setNotice(operation === "probe" ? "Проверка выполнена. Показаны только результаты этой проверки." : "Состояние маршрута обновлено.");
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Операция с маршрутом прокси не выполнена.");
    } finally {
      setBusy(false);
    }
  };

  const saveProfile = async () => {
    if (!state) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const next = await save({ ...state.profile, desired_state: desiredState });
      setState(next);
      setDesiredState(next.profile.desired_state);
      setNotice("Желаемое состояние сохранено. Чтобы применить его, используйте действие включения или отключения.");
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Не удалось сохранить настройку прокси.");
    } finally {
      setBusy(false);
    }
  };

  const probe = state?.runtime.last_probe;
  return <div className="page-shell native-operation-page">
    <PageHeader eyebrow="СЕТЕВОЙ МАРШРУТ" title="Прокси через сервер 44" description="Постоянный маршрут для запросов с VUSA через немецкий выход node-44." actions={<StatusBadge state={error ? "error" : state?.runtime.service_state === "active" ? "ready" : "neutral"}>{busy ? "Выполняем…" : state ? serviceLabel(state.runtime.service_state) : "Загружаю…"}</StatusBadge>} />
    <ErrorNotice message={error} />
    {notice && <div className="state-panel card standalone-state compact-notice" role="status">{notice}</div>}
    {!state ? <section className="card compact-empty"><strong>Состояние маршрута недоступно</strong><span>Обновите страницу или повторите загрузку.</span><button className="button secondary" type="button" disabled={busy} onClick={() => void refresh()}>Загрузить состояние</button></section> : <>
      <section className="card">
        <div className="card-heading"><div><p className="section-kicker">НАСТРОЙКА</p><h2>Маршрут VUSA → node-44</h2></div><StatusBadge state={state.runtime.listener_state === "listening" ? "ready" : state.runtime.listener_state === "closed" ? "neutral" : "error"}>{listenerLabel(state.runtime.listener_state)}</StatusBadge></div>
        <dl className="proxy-route-details">
          <div><dt>Выход</dt><dd>Германия · HTTP proxy · node-44</dd></div>
          <div><dt>Локальный адрес VUSA</dt><dd>127.0.0.1:24836</dd></div>
          <div><dt>SSH профиль</dt><dd>relay02 (защищённый системный ключ)</dd></div>
          <div><dt>Профиль</dt><dd>{state.runtime.profile_saved ? "Сохранён" : "Значения по умолчанию, ещё не сохранены"}</dd></div>
          <div><dt>Сервис</dt><dd>{serviceLabel(state.runtime.service_state)}</dd></div>
        </dl>
        <label className="proxy-route-desired"><span>Сохранённое желаемое состояние</span><select value={desiredState} disabled={busy} onChange={(event) => setDesiredState(event.target.value as ProxyRouteProfile["desired_state"])}><option value="disabled">Выключен</option><option value="enabled">Включён</option></select></label>
        <div className="row-actions proxy-route-actions">
          <button className="button secondary" type="button" disabled={busy || (state.runtime.profile_saved && desiredState === state.profile.desired_state)} onClick={() => void saveProfile()}>Сохранить состояние</button>
          <button className="button primary" type="button" disabled={busy} onClick={() => void runAction("enable")}>Включить</button>
          <button className="button secondary" type="button" disabled={busy} onClick={() => void runAction("disable")}>Отключить</button>
          <button className="button secondary" type="button" disabled={busy} onClick={() => void runAction("reconcile")}>Применить сохранённое</button>
          <button className="button secondary" type="button" disabled={busy} onClick={() => void runAction("probe")}>Проверить маршрут</button>
        </div>
        <p className="muted-help">Сохранение меняет желаемое состояние, но не запускает соединение. Включение и отключение управляют постоянным системным сервисом; проверка выполняет отдельный ограниченный запрос.</p>
      </section>
      <section className="card proxy-route-probe">
        <div className="card-heading"><div><p className="section-kicker">ПОСЛЕДНЯЯ ПРОВЕРКА</p><h2>Фактический выход</h2></div><StatusBadge state={probe?.server44_path_verified === true ? "ready" : probe?.server44_path_verified === false ? "error" : "neutral"}>{probe ? pathLabel(probe.server44_path_verified) : "Не проверено"}</StatusBadge></div>
        <p className="muted-help">{formatTime(probe?.checked_at ?? null)}</p>
        {probe ? <dl className="proxy-route-details">
          <div><dt>Прямой выход VUSA</dt><dd>{probe.direct_exit_ip ?? "Не получен"}</dd></div>
          <div><dt>Выход через прокси</dt><dd>{probe.proxy_exit_ip ?? "Не получен"}</dd></div>
          <div className="proxy-route-diagnostic"><dt>Проверка соединения с node-44</dt><dd>{probe.server44_path_reason || (probe.server44_path_verified === true ? "Есть подтверждение установленного соединения." : probe.server44_path_verified === false ? "Нет подтверждения установленного соединения." : "Данных для подтверждения нет.")}</dd></div>
          <div><dt>Итог проверки</dt><dd>{probe.state}</dd></div>
        </dl> : <div className="compact-empty"><strong>Нет данных проверки</strong><span>Нажмите «Проверить маршрут», чтобы получить текущие адреса и проверить фактический путь через node-44.</span></div>}
      </section>
    </>}
  </div>;
}
