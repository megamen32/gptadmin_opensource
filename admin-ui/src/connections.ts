import type { ClientInventoryItem } from "./api";

export type Connection = ClientInventoryItem & {
  label: string;
  credentials: ClientInventoryItem[];
  registered: boolean;
};

export function shortId(id: string): string {
  const value = id.replace(/^gptadmin-/, "");
  return value.length > 20 ? `${value.slice(0, 10)}…${value.slice(-6)}` : value;
}

function oauthLabel(item: ClientInventoryItem): string {
  const chatgpt = item.redirect_uris.some((value) => {
    try { return ["chatgpt.com", "chat.openai.com"].includes(new URL(value).hostname); } catch { return false; }
  });
  return `${chatgpt ? "ChatGPT" : "OAuth"} · ${shortId(item.client_id)}`;
}

// The raw endpoint also serves diagnostics. In the owner UI one OAuth client
// is a connection; its rotating refresh credentials belong in read-only history.
export function logicalConnections(records: ClientInventoryItem[]): Connection[] {
  const oauth = new Map<string, Connection>();
  const result: Connection[] = [];
  for (const record of records) {
    if (record.token_kind === "oauth") {
      oauth.set(record.client_id, { ...record, id: record.client_id, label: oauthLabel(record), credentials: [], registered: true });
    } else if (record.token_kind !== "oauth_refresh") {
      result.push({ ...record, label: record.client_id, credentials: [], registered: true });
    }
  }
  for (const record of records.filter((item) => item.token_kind === "oauth_refresh")) {
    let connection = oauth.get(record.client_id);
    if (!connection) {
      connection = { ...record, id: record.client_id, token_kind: "oauth", status: "unregistered", role: "client", profile_id: null, label: `OAuth · ${shortId(record.client_id)}`, credentials: [], registered: false };
      oauth.set(record.client_id, connection);
    }
    connection.credentials.push(record);
  }
  for (const connection of oauth.values()) {
    connection.credentials.sort((a, b) => (b.issued_at ?? 0) - (a.issued_at ?? 0) || a.id.localeCompare(b.id));
    result.push(connection);
  }
  return result.sort((a, b) => a.label.localeCompare(b.label) || a.id.localeCompare(b.id));
}
