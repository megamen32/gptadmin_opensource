import { NextRequest, NextResponse } from 'next/server';

// Thin async adapter over the local GrepMesh Console API (loopback-only),
// see grepmesh src/console.rs: POST /api/search starts a search job and
// returns its first status page; POST /api/search/status { job_id } returns
// a status page with state "running" | "complete" | "failed" | "lost" | "expired".
const CONSOLE = 'http://127.0.0.1:9419';
// MVP selected host set: wildcard "*" is intentionally never sent from here so
// one unhealthy host cannot widen or stall the fan-out.
const DEFAULT_HOSTS = ['server-100', 'server-88'];
// The mesh's own overall search timeout is 20s; poll until then plus margin.
const POLL_DEADLINE_MS = 30_000;
const SEARCH_TIMEOUT_MS = 10_000;
const STATUS_TIMEOUT_MS = 10_000;
const INITIAL_POLL_DELAY_MS = 500;
const MAX_POLL_DELAY_MS = 1_000;
const MAX_MATCHES_CAP = 200;
const MAX_WAIT_MS = 5_000;

interface ConsolePayload {
  state?: unknown;
  job_id?: unknown;
  [field: string]: unknown;
}

function invalid(message: string) {
  return NextResponse.json({ error: message }, { status: 400 });
}

function runtimeFailure(message: string) {
  return NextResponse.json({ error: `GrepMesh console: ${message}` }, { status: 502 });
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function postConsole(path: string, body: unknown, timeoutMs: number) {
  let response: Response;
  try {
    response = await fetch(`${CONSOLE}${path}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(timeoutMs),
    });
  } catch (error) {
    throw new Error(`transport failure calling ${path}: ${error instanceof Error ? error.message : String(error)}`);
  }
  const text = await response.text();
  let json: unknown = null;
  if (text) {
    try {
      json = JSON.parse(text);
    } catch {
      json = null;
    }
  }
  return { status: response.status, ok: response.ok, json, text };
}

// Non-2xx from the console is passed through verbatim (it already carries a
// JSON {"error": ...} body); only unparseable error bodies become 502.
function errorResponse(status: number, json: unknown, text: string) {
  if (json && typeof json === 'object') return NextResponse.json(json, { status });
  return runtimeFailure(`HTTP ${status}: ${text.slice(0, 200) || 'empty body'}`);
}

function asPayload(json: unknown): ConsolePayload | null {
  if (!json || typeof json !== 'object' || Array.isArray(json)) return null;
  const payload = json as ConsolePayload;
  return typeof payload.state === 'string' ? payload : null;
}

export async function POST(request: NextRequest) {
  const body = await request.json().catch(() => null);
  if (!body || typeof body !== 'object' || Array.isArray(body)) return invalid('Request body must be a JSON object');

  const { query, hosts, max_matches, wait_ms } = body as Record<string, unknown>;
  if (typeof query !== 'string' || !query.trim()) return invalid('Query is required');

  let hostList = DEFAULT_HOSTS;
  if (hosts !== undefined) {
    if (!Array.isArray(hosts) || hosts.length === 0 || hosts.some((host) => typeof host !== 'string' || !host.trim() || host.trim() === '*')) {
      return invalid('hosts must be a non-empty array of host names ("*" is not allowed)');
    }
    hostList = hosts.map((host) => host.trim());
  }

  if (max_matches !== undefined && (typeof max_matches !== 'number' || !Number.isInteger(max_matches) || max_matches < 1 || max_matches > MAX_MATCHES_CAP)) {
    return invalid(`max_matches must be an integer between 1 and ${MAX_MATCHES_CAP}`);
  }
  if (wait_ms !== undefined && (typeof wait_ms !== 'number' || !Number.isInteger(wait_ms) || wait_ms < 0 || wait_ms > MAX_WAIT_MS)) {
    return invalid(`wait_ms must be an integer between 0 and ${MAX_WAIT_MS}`);
  }

  // /api/search body = SearchArgs (grepmesh src/mcp.rs); max_matches is the
  // accepted alias of limit. wait_ms is forwarded as advisory: current console
  // builds only honor it on the MCP tool layer, so the adapter below always
  // polls /api/search/status until a terminal state.
  const searchBody: Record<string, unknown> = { query: query.trim(), hosts: hostList, verbose: true };
  if (max_matches !== undefined) searchBody.max_matches = max_matches;
  if (wait_ms !== undefined) searchBody.wait_ms = wait_ms;

  let current: ConsolePayload;
  try {
    const search = await postConsole('/api/search', searchBody, SEARCH_TIMEOUT_MS);
    if (!search.ok) return errorResponse(search.status, search.json, search.text);
    const payload = asPayload(search.json);
    if (!payload) return runtimeFailure('malformed /api/search response (missing state)');
    if (payload.state !== 'running') return NextResponse.json({ data: payload });
    if (typeof payload.job_id !== 'string' || !payload.job_id) return runtimeFailure('malformed /api/search response: state=running without job_id');
    current = payload;
  } catch (error) {
    return runtimeFailure(error instanceof Error ? error.message : String(error));
  }

  // Poll until terminal; a deadline-exceeded running page is still returned
  // verbatim (honest state, job_id included) rather than faked as complete.
  const deadline = Date.now() + POLL_DEADLINE_MS;
  let delay = INITIAL_POLL_DELAY_MS;
  while (Date.now() < deadline) {
    await sleep(delay);
    delay = Math.min(delay * 2, MAX_POLL_DELAY_MS);
    try {
      const status = await postConsole('/api/search/status', { job_id: current.job_id }, STATUS_TIMEOUT_MS);
      if (!status.ok) return errorResponse(status.status, status.json, status.text);
      const payload = asPayload(status.json);
      if (!payload) return runtimeFailure('malformed /api/search/status response (missing state)');
      current = payload;
      if (payload.state !== 'running') break;
    } catch (error) {
      return runtimeFailure(error instanceof Error ? error.message : String(error));
    }
  }
  return NextResponse.json({ data: current });
}
