// Absolute public-asset paths are not rewritten by vite base; route them
// through BASE_URL so the desktop also works below /cloudos/.
export function asset(path: string): string {
	const base = import.meta.env.BASE_URL ?? '/';
	const prefix = base.endsWith('/') ? base : `${base}/`;
	return `${prefix}${path.replace(/^\/+/, '')}`;
}
