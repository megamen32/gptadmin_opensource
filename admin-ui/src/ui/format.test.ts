import { describe, expect, it } from "vitest";
import { EMPTY_TIMESTAMP, formatRelativeTimestamp, formatServerLastSeen, formatTimestamp, serverRowQualifier, serverStatusLabel } from "./format";

const SECONDS = 1784917200;
const MILLISECONDS = SECONDS * 1000;
function localTimestamp(milliseconds: number): string {
  return new Date(milliseconds).toLocaleString("ru-RU", {
    day: "2-digit",
    month: "2-digit",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}
const SAME_MOMENT = localTimestamp(MILLISECONDS);
const ISO_MILLISECONDS = new Date(MILLISECONDS).toISOString();
const ISO_NANOSECONDS = ISO_MILLISECONDS.replace(/\.(\d{3})Z$/, ".$1182732Z");

describe("formatTimestamp", () => {
  it("formats ISO 8601 strings", () => {
    expect(formatTimestamp(ISO_MILLISECONDS)).toBe(SAME_MOMENT);
    // The local date can cross midnight in Moscow while remaining October 8 in UTC.
    expect(formatTimestamp("2026-10-08T21:55:23.154Z")).toBe(
      localTimestamp(Date.UTC(2026, 9, 8, 21, 55, 23, 154)),
    );
  });

  it("formats ISO 8601 strings with nanosecond precision", () => {
    expect(formatTimestamp(ISO_NANOSECONDS)).toBe(SAME_MOMENT);
  });

  it("formats Unix seconds", () => {
    expect(formatTimestamp(SECONDS)).toBe(SAME_MOMENT);
  });

  it("formats Unix milliseconds", () => {
    expect(formatTimestamp(MILLISECONDS)).toBe(SAME_MOMENT);
  });

  it("formats all-digit strings as Unix timestamps", () => {
    expect(formatTimestamp(String(SECONDS))).toBe(SAME_MOMENT);
    expect(formatTimestamp(String(MILLISECONDS))).toBe(SAME_MOMENT);
  });

  it("formats the epoch itself instead of treating it as missing", () => {
    expect(formatTimestamp(0)).toBe(new Date(0).toLocaleString("ru-RU", {
      day: "2-digit", month: "2-digit", year: "numeric",
      hour: "2-digit", minute: "2-digit", second: "2-digit",
    }));
    expect(formatTimestamp(0)).not.toBe(EMPTY_TIMESTAMP);
    expect(formatTimestamp("0")).toBe(formatTimestamp(0));
  });

  it("returns the placeholder for null and undefined", () => {
    expect(formatTimestamp(null)).toBe(EMPTY_TIMESTAMP);
    expect(formatTimestamp(undefined)).toBe(EMPTY_TIMESTAMP);
    expect(formatTimestamp("")).toBe(EMPTY_TIMESTAMP);
    expect(formatTimestamp("   ")).toBe(EMPTY_TIMESTAMP);
  });

  it("returns the placeholder for unparseable garbage instead of throwing", () => {
    for (const garbage of ["не дата", "NaN", "--", "12:00", {}, []] as unknown[]) {
      expect(() => formatTimestamp(garbage as string)).not.toThrow();
      expect(formatTimestamp(garbage as string)).toBe(EMPTY_TIMESTAMP);
    }
    expect(formatTimestamp(Number.NaN)).toBe(EMPTY_TIMESTAMP);
    expect(formatTimestamp(Number.POSITIVE_INFINITY)).toBe(EMPTY_TIMESTAMP);
  });

  it("includes seconds so freshly written values stay distinguishable", () => {
    expect(formatTimestamp(MILLISECONDS)).toMatch(/\d{2}:\d{2}:\d{2}/);
  });
});

describe("formatRelativeTimestamp", () => {
  const NOW = Date.UTC(2026, 9, 9, 12, 0, 0);
  const ago = (seconds: number): number => NOW - seconds * 1000;

  it("reports a just-polled node as «только что» instead of a ticking clock", () => {
    expect(formatRelativeTimestamp(ago(0), NOW)).toBe("только что");
    expect(formatRelativeTimestamp(ago(9), NOW)).toBe("только что");
  });

  it("counts seconds with Russian plurals", () => {
    expect(formatRelativeTimestamp(ago(15), NOW)).toBe("15 секунд назад");
    expect(formatRelativeTimestamp(ago(21), NOW)).toBe("21 секунду назад");
    expect(formatRelativeTimestamp(ago(23), NOW)).toBe("23 секунды назад");
  });

  it("counts minutes with Russian plurals", () => {
    expect(formatRelativeTimestamp(ago(60), NOW)).toBe("1 минуту назад");
    expect(formatRelativeTimestamp(ago(150), NOW)).toBe("3 минуты назад");
    expect(formatRelativeTimestamp(ago(540), NOW)).toBe("9 минут назад");
  });

  it("falls back to the absolute timestamp beyond the relative window", () => {
    const old = ago(3600);
    expect(formatRelativeTimestamp(old, NOW)).toBe(formatTimestamp(old));
    expect(formatRelativeTimestamp(old, NOW)).not.toMatch(/назад/);
  });

  it("treats a future timestamp from clock skew as «только что»", () => {
    expect(formatRelativeTimestamp(NOW + 5000, NOW)).toBe("только что");
  });

  it("returns the placeholder for missing and unparseable values", () => {
    expect(formatRelativeTimestamp(null, NOW)).toBe(EMPTY_TIMESTAMP);
    expect(formatRelativeTimestamp(undefined, NOW)).toBe(EMPTY_TIMESTAMP);
    expect(formatRelativeTimestamp("", NOW)).toBe(EMPTY_TIMESTAMP);
    expect(formatRelativeTimestamp("не дата", NOW)).toBe(EMPTY_TIMESTAMP);
    expect(formatRelativeTimestamp(Number.NaN, NOW)).toBe(EMPTY_TIMESTAMP);
  });
});

describe("formatServerLastSeen", () => {
  const NOW = Date.now();
  const recent = NOW - 90 * 1000;
  const old = NOW - 7200 * 1000;

  it("uses relative wording while the node is online", () => {
    expect(formatServerLastSeen("online", recent)).toBe("2 минуты назад");
  });

  it("keeps the exact time for stale and failed nodes, where the stamp is the signal", () => {
    expect(formatServerLastSeen("stale", old)).toBe(formatTimestamp(old));
    expect(formatServerLastSeen("failed", old)).toBe(formatTimestamp(old));
  });
});

describe("serverStatusLabel", () => {
  it("localises failed instead of leaking the raw English status", () => {
    expect(serverStatusLabel("failed")).toBe("Ошибка");
    expect(serverStatusLabel("failed")).not.toBe("failed");
  });

  it("keeps the existing Russian labels and the unknown fallback", () => {
    expect(serverStatusLabel("online")).toBe("Работает");
    expect(serverStatusLabel("offline")).toBe("Недоступен");
    expect(serverStatusLabel("stale")).toBe("Давно не отвечает");
    expect(serverStatusLabel(undefined)).toBe("Неизвестно");
  });
});

describe("serverRowQualifier", () => {
  it("names the file target of a host so it is not confused with the host itself", () => {
    expect(serverRowQualifier("file:admin-node-100", "Files: admin-node-100", "virtual_file", {})).toBe("Файлы");
    // The bare shell node needs no qualifier: its friendly name is already unambiguous.
    expect(serverRowQualifier("shell:admin-node-100", "Shell: admin-node-100", "virtual_shell", {})).toBe("");
  });

  it("adds the child MCP reference to the inherited parent name", () => {
    expect(serverRowQualifier(
      "mcp:shell:admin-node-100:grepmesh",
      "grepmesh on admin-node-100",
      "child_mcp",
      { child_ref: "grepmesh" },
    )).toBe("MCP-компонент: grepmesh");
  });

  it("falls back to the descriptor name when child_ref is missing", () => {
    expect(serverRowQualifier(
      "mcp:shell:admin-node-100:agent-browser",
      "agent-browser on admin-node-100",
      "child_mcp",
      {},
    )).toBe("MCP-компонент: agent-browser");
  });

  it("labels standalone MCP servers, virtual MCPs and the hub", () => {
    expect(serverRowQualifier("admin-node-100-browseros", "BrowserOS on admin-node-100", "real_mcp", {})).toBe("MCP-сервер: BrowserOS");
    expect(serverRowQualifier("network-proxy", "Network Proxy", "virtual_mcp", {})).toBe("Встроенный MCP");
    expect(serverRowQualifier("hub", "GPTAdmin Hub", "hub", {})).toBe("Хаб");
  });

  it("never repeats the friendly name as the qualifier", () => {
    expect(serverRowQualifier("hub", "Хаб", "hub", {})).toBe("Хаб");
    expect(serverRowQualifier("grepmesh", "grepmesh", "child_mcp", { child_ref: "grepmesh" })).toBe("MCP-компонент");
  });
});