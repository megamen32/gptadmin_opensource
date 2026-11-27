export function friendlyServerName(serverId?: string, name?: string): string {
  const id = serverId || "";
  const raw = name || id.replace(/^shell:/, "");
  const match = raw.match(/(?:admin-node-|node-)(\d+)$/);
  return match ? `Сервер ${match[1]}` : raw || "Без имени";
}

/**
 * Короткое и честное уточнение к строке списка: по нему видно, что за узел
 * стоит за одинаковым «Сервер 100» — сама машина, её файловый таргет или
 * дочерний MCP-компонент. Пустая строка означает, что уточнять нечего.
 */
export function serverRowQualifier(serverId?: string, name?: string, kind?: string, meta?: Record<string, unknown>): string {
  const primary = friendlyServerName(serverId, name);
  const withComponent = (label: string, component: string): string => component && component !== primary ? `${label}: ${component}` : label;
  const fromName = (): string => {
    const trimmed = (name || "").trim();
    if (!trimmed || trimmed === primary) return "";
    const component = trimmed.replace(/\s+(?:on|via)\s+.+$/i, "").trim();
    return component && component !== primary ? component : "";
  };
  const ref = typeof meta?.child_ref === "string" ? meta.child_ref.trim() : "";
  switch (kind) {
    case "virtual_file": return "Файлы";
    case "child_mcp": return withComponent("MCP-компонент", ref || fromName());
    case "real_mcp": return withComponent("MCP-сервер", fromName());
    case "virtual_mcp": return "Встроенный MCP";
    case "hub": return "Хаб";
    default: return "";
  }
}

export function serverStatusLabel(status?: string): string {
  if (status === "online") return "Работает";
  if (status === "offline") return "Недоступен";
  if (status === "stale") return "Давно не отвечает";
  if (status === "failed") return "Ошибка";
  if (status === "local") return "На этом сервере";
  return status || "Неизвестно";
}

export function jobStatusLabel(status?: string): string {
  if (status === "completed") return "Готово";
  if (status === "failed" || status === "error") return "Ошибка";
  if (status === "running") return "Выполняется";
  if (status?.startsWith("queued")) return "В очереди";
  if (status === "cancelled") return "Отменено";
  return status || "Неизвестно";
}

export function friendlyActionName(toolName?: string): string {
  const name = toolName || "";
  if (!name) return "Системная операция";
  if (name === "shell_exec" || name.includes("shell")) return "Команда на сервере";
  if (name.startsWith("file_") || name.includes("file")) return "Работа с файлами";
  if (name.includes("service") || name.includes("systemd")) return "Управление сервисом";
  if (name.includes("backup")) return "Резервная копия";
  if (name.includes("mcp")) return "Операция MCP";
  return name.replaceAll("_", " ");
}

/** Значения ниже этого порога считаем Unix-секундами; современные миллисекунды — около 1.7e12. */
const UNIX_MILLISECONDS_THRESHOLD = 1e11;

/** Плейсхолдер для пустого или нечитаемого значения. */
export const EMPTY_TIMESTAMP = "—";

const TIMESTAMP_FORMAT: Intl.DateTimeFormatOptions = {
  day: "2-digit",
  month: "2-digit",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
};

function fromEpoch(value: number): Date | null {
  if (!Number.isFinite(value)) return null;
  const date = new Date(Math.abs(value) < UNIX_MILLISECONDS_THRESHOLD ? value * 1000 : value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function fromString(value: string): Date | null {
  const raw = value.trim();
  if (!raw) return null;
  if (/^[+-]?\d+$/.test(raw)) return fromEpoch(Number(raw));
  // Даты с наносекундной точностью нужно урезать до миллисекунд: парсер не обязан понимать лишние цифры.
  const date = new Date(raw.replace(/(\.\d{3})\d+/, "$1"));
  return Number.isNaN(date.getTime()) ? null : date;
}

/**
 * Локальная дата и время пользователя для любого формата, который отдаёт бэкенд:
 * ISO 8601 (включая наносекунды), Unix-секунды, Unix-миллисекунды и строки из цифр.
 * На пустое или нечитаемое значение возвращает «—» и никогда не бросает исключений.
 */
export function formatTimestamp(value: string | number | null | undefined): string {
  try {
    if (value === null || value === undefined) return EMPTY_TIMESTAMP;
    const date = typeof value === "number" ? fromEpoch(value) : fromString(value);
    return date ? date.toLocaleString("ru-RU", TIMESTAMP_FORMAT) : EMPTY_TIMESTAMP;
  } catch {
    return EMPTY_TIMESTAMP;
  }
}

/** Точная исходная метка времени для подсказки `title`. */
export function timestampTitle(value: string | number | null | undefined): string {
  if (value === null || value === undefined) return EMPTY_TIMESTAMP;
  const raw = typeof value === "string" ? value.trim() : String(value);
  return raw || EMPTY_TIMESTAMP;
}

/** Ниже этого порога «когда» ещё значимо говорить словами, дальше — точные часы. */
const RELATIVE_MAX_AGE_SECONDS = 600;
const JUST_NOW_SECONDS = 10;

function pluralRu(count: number, one: string, few: string, many: string): string {
  const mod100 = Math.abs(count) % 100;
  const mod10 = mod100 % 10;
  if (mod100 >= 11 && mod100 <= 14) return many;
  if (mod10 === 1) return one;
  if (mod10 >= 2 && mod10 <= 4) return few;
  return many;
}

/**
 * «Только что», «N секунд/минут назад», а дальше — абсолютные часы.
 * У живого узла `last_seen` совпадает с текущим временем опроса, поэтому абсолютная
 * метка там читается как счётчик секунд и не отвечает на вопрос «когда машина отвечала».
 * Часы в относительном виде недостижимы: окно относительности — 10 минут, после него
 * точное время полезнее, чем «N часов назад».
 */
export function formatRelativeTimestamp(value: string | number | null | undefined, now: number = Date.now()): string {
  try {
    if (value === null || value === undefined) return EMPTY_TIMESTAMP;
    const date = typeof value === "number" ? fromEpoch(value) : fromString(value);
    if (!date) return EMPTY_TIMESTAMP;
    const ageSeconds = Math.round((now - date.getTime()) / 1000);
    if (ageSeconds < JUST_NOW_SECONDS) return "только что";
    if (ageSeconds < 60) return `${ageSeconds} ${pluralRu(ageSeconds, "секунду", "секунды", "секунд")} назад`;
    const minutes = Math.round(ageSeconds / 60);
    if (minutes * 60 < RELATIVE_MAX_AGE_SECONDS) return `${minutes} ${pluralRu(minutes, "минуту", "минуты", "минут")} назад`;
    return formatTimestamp(value);
  } catch {
    return EMPTY_TIMESTAMP;
  }
}

/** A zero/missing last_seen is the Hub's "never polled" sentinel, not a reply at the Unix epoch. */
export function hasServerLastSeen(value: string | number | null | undefined): boolean {
  if (value === null || value === undefined) return false;
  if (typeof value === "string" && !value.trim()) return false;
  const date = typeof value === "number" ? fromEpoch(value) : fromString(value);
  return date !== null && date.getTime() > 0;
}

/**
 * Метка «последнего ответа» с учётом состояния узла: у работающего сервера
 * `last_seen` догоняет текущие часы, поэтому показываем «N минут назад»,
 * а у stale/failed метка и есть сигнал — там оставляем точное время.
 */
export function formatServerLastSeen(status: string | undefined, value: string | number | null | undefined): string {
  if (!hasServerLastSeen(value)) return "Ещё не отвечал";
  return status === "online" ? formatRelativeTimestamp(value) : formatTimestamp(value);
}
