// GPTAdmin CloudOS — Hub API client.
// CloudOS UI is only a client of the GPTAdmin hub; it never talks to nodes
// directly and owns no backend of its own.

const BASE = '/api/v1/cloud-os';

export type CloudComputer = {
	id: string;
	name: string;
	os: string;
	capabilities: string[];
	status: string;
	last_seen: number | string;
	/** node = unified-node/ShellMCP executor (default fleet), agent = direct Cloud OS agent */
	kind: 'node' | 'agent';
};

export type CloudFileEntry = {
	name: string;
	is_dir?: boolean;
	size?: number;
	mode?: string;
	mtime?: string | number;
};

function normalize_entry(raw: Record<string, any>): CloudFileEntry {
	const type = typeof raw.type === 'string' ? raw.type.toLowerCase() : '';
	return {
		name: String(raw.name ?? ''),
		is_dir: raw.is_dir ?? (type === 'directory' || type === 'dir'),
		size: typeof raw.size === 'number' ? raw.size : undefined,
		mode: typeof raw.mode === 'string' ? raw.mode : undefined,
		mtime: raw.modified_at ?? raw.mtime,
	};
}

export type BrowserConnector = {
	id: string;
	name: string;
	status: string;
	parent: string;
};

async function getJSON<T>(url: string, timeout_ms = 10000): Promise<T> {
	const res = await fetch(url, { signal: AbortSignal.timeout(timeout_ms) });
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	return (await res.json()) as T;
}

async function postJSON<T>(url: string, body: unknown, timeout_ms = 40000): Promise<T> {
	const res = await fetch(url, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(body),
		signal: AbortSignal.timeout(timeout_ms),
	});
	const payload = await res.json().catch(() => null);
	if (!res.ok) {
		const detail = payload && (payload.detail || payload.error);
		throw new Error(typeof detail === 'string' ? detail : `HTTP ${res.status}`);
	}
	return payload as T;
}

/** Every unified node registers a shell executor and appears here by default. */
async function listShellComputers(): Promise<CloudComputer[]> {
	const payload = await getJSON<{ computers: Array<Record<string, unknown>> }>(
		`${BASE}/shell/computers`,
		6000,
	);
	return (payload.computers ?? []).map((c) => ({
		id: String(c.id ?? ''),
		name: String(c.name ?? c.id ?? ''),
		os: String(c.os ?? ''),
		capabilities: Array.isArray(c.capabilities) ? c.capabilities.map(String) : [],
		status: String(c.status ?? 'unknown'),
		last_seen: (c.last_seen as number | string) ?? '',
		kind: 'node' as const,
	}));
}

/** Direct Cloud OS agents paired through the registry. */
async function listRegistryComputers(): Promise<CloudComputer[]> {
	try {
		const payload = await getJSON<{ computers: Array<Record<string, unknown>> }>(
			`${BASE}/computers`,
			6000,
		);
		return (payload.computers ?? []).map((c) => ({
			id: String(c.id ?? ''),
			name: String(c.name ?? c.id ?? ''),
			os: String(c.os ?? ''),
			capabilities: Array.isArray(c.capabilities) ? c.capabilities.map(String) : [],
			status: String(c.status ?? 'unknown'),
			last_seen: (c.last_seen as number | string) ?? '',
			kind: 'agent' as const,
		}));
	} catch {
		// The registry is optional for the desktop; nodes alone are enough.
		return [];
	}
}

export async function listComputers(): Promise<CloudComputer[]> {
	const [nodes, agents] = await Promise.all([listShellComputers(), listRegistryComputers()]);
	const seen = new Set(nodes.map((n) => n.id));
	return [...nodes, ...agents.filter((a) => !seen.has(a.id))].sort((a, b) => a.name.localeCompare(b.name));
}

/** Dig the shell job payload for printable output across response generations. */
function extractJobOutput(payload: unknown): string {
	const p = payload as Record<string, any> | null;
	if (!p) return '';
	if (typeof p.error === 'string' && p.error) return p.error;
	const result = p?.response?.structuredContent?.result ?? p?.structuredContent?.result ?? p?.result;
	if (result == null) {
		const text = p?.response?.content?.[0]?.text ?? p?.content?.[0]?.text;
		return typeof text === 'string' ? text : '';
	}
	if (typeof result === 'string') return result;
	const stdout = result.stdout ?? result.output ?? '';
	const stderr = result.stderr ?? '';
	const text = result.content?.[0]?.text;
	const nested = result.result?.stdout ?? result.result?.content?.[0]?.text ?? '';
	const out = [String(stdout || ''), String(nested || ''), typeof text === 'string' ? text : '']
		.filter(Boolean)
		.join('\n')
		.trim();
	const err = String(stderr || '').trim();
	if (out || err) return err && !out ? err : [out, err].filter(Boolean).join('\n');
	return JSON.stringify(result, null, 2);
}

export async function execCommand(target: string, cmd: string, cwd = '', timeout = 25): Promise<string> {
	const payload = await postJSON<Record<string, any>>(
		`${BASE}/shell/exec`,
		{ target, cmd, cwd: cwd || undefined, timeout },
		(timeout + 10) * 1000,
	);
	if (payload?.status === 'completed' || payload?.status === 'failed' || payload?.status === 'cancelled') {
		return extractJobOutput(payload) || `job ${payload.status}`;
	}
	if (payload?.background) {
		return `⏳ job ${payload.job_id} still running on ${target}`;
	}
	return extractJobOutput(payload) || JSON.stringify(payload, null, 2);
}

function parseEntries(result: unknown): CloudFileEntry[] {
	if (result == null) return [];
	if (Array.isArray(result)) {
		return result.map((e) =>
			typeof e === 'string' ? { name: e } : normalize_entry(e as Record<string, any>),
		);
	}
	const r = result as Record<string, any>;
	const raw =
		r.entries ??
		r.result?.entries ??
		r.items ??
		r.result?.items ??
		r.inspection?.entries ??
		[];
	let entries: CloudFileEntry[] = Array.isArray(raw)
		? raw.map((e: unknown) => (typeof e === 'string' ? { name: e } : normalize_entry(e as Record<string, any>)))
		: [];
	if (entries.length === 0) {
		const text = r.content?.[0]?.text ?? r.result?.content?.[0]?.text;
		if (typeof text === 'string' && text.trim().startsWith('[')) {
			try {
				entries = JSON.parse(text);
			} catch {
				/* keep empty */
			}
		} else if (typeof text === 'string' && text.trim().startsWith('{')) {
			try {
				entries = parseEntries(JSON.parse(text));
			} catch {
				/* keep empty */
			}
		}
	}
	return entries;
}

export async function listPath(
	target: string,
	path: string,
): Promise<{ path: string; entries: CloudFileEntry[] }> {
	const payload = await postJSON<Record<string, any>>(`${BASE}/shell/inspect`, { target, path });
	const result =
		payload?.response?.structuredContent?.result ?? payload?.structuredContent?.result ?? payload?.result;
	const error = result?.error ?? result?.structuredContent?.inspection?.error;
	if (typeof error === 'string' && error) {
		throw new Error(error);
	}
	const inspection = result?.structuredContent?.inspection ?? result?.inspection;
	const resolved = typeof inspection?.path === 'string' ? inspection.path : path;
	const entries = Array.isArray(inspection?.entries)
		? inspection.entries.map((e: unknown) => normalize_entry(e as Record<string, any>))
		: (result?.entries ?? parseEntries(result));
	return { path: resolved, entries };
}

export async function listBrowserConnectors(): Promise<BrowserConnector[]> {
	const payload = await getJSON<{ connectors: BrowserConnector[] }>(
		`${BASE}/browser/connectors`,
		6000,
	);
	return payload.connectors ?? [];
}

export async function loadBrowserTabs(connector: string): Promise<string> {
	const payload = await postJSON<Record<string, any>>(`${BASE}/browser/tabs`, { connector }, 30000);
	const text =
		payload?.response?.structuredContent?.result?.structuredContent?.result?.content?.[0]?.text ??
		payload?.response?.structuredContent?.result?.content?.[0]?.text ??
		payload?.response?.content?.[0]?.text;
	if (typeof text === 'string' && text) return text;
	return JSON.stringify(payload, null, 2);
}
