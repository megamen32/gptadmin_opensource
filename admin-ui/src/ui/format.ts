export function friendlyServerName(serverId?: string, name?: string): string {
  const id = serverId || "";
  const raw = name || id.replace(/^shell:/, "");
  const match = raw.match(/(?:admin-server-|server-)(\d+)$/);
  return match ? `Сервер ${match[1]}` : raw || "Без имени";
}

export function serverStatusLabel(status?: string): string {
  if (status === "online") return "Работает";
  if (status === "offline") return "Недоступен";
  if (status === "stale") return "Давно не отвечает";
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
