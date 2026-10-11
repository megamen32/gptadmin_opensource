<script lang="ts">
	import { onMount } from 'svelte';
	import {
		listComputers,
		listPath,
		type CloudComputer,
		type CloudFileEntry,
	} from '🍎/lib/cloudos-api.ts';

	let computers = $state<CloudComputer[]>([]);
	let selected = $state('');
	let path = $state('/');
	let path_input = $state('/');
	let entries = $state<CloudFileEntry[]>([]);
	let message = $state('');
	let loading = $state(false);

	const computer = $derived(computers.find((c) => c.id === selected));
	const sorted = $derived(
		[...entries].sort((a, b) => {
			const ad = a.is_dir ?? false;
			const bd = b.is_dir ?? false;
			if (ad !== bd) return ad ? -1 : 1;
			return (a.name ?? '').localeCompare(b.name ?? '');
		}),
	);

	onMount(async () => {
		try {
			computers = (await listComputers()).filter(
				(c) => c.status === 'online' && c.capabilities.includes('files'),
			);
			selected = computers[0]?.id ?? '';
			await default_root();
		} catch {
			message = 'Hub unreachable.';
		}
	});

	/** Inspection roots are configured per node; land on the first listing. */
	async function default_root(): Promise<void> {
		const candidates = ['/home/admin', '/home', '/root', '/var/lib/gptadmin/workspaces', '/var/lib/gptadmin', '/'];
		for (const computer of computers.slice(0, 4)) {
			for (const candidate of candidates) {
				try {
					const result = await listPath(computer.id, candidate);
					selected = computer.id;
					path = result.path || candidate;
					path_input = path;
					entries = result.entries;
					message = `${result.entries.length} entries from ${computer.name}.`;
					return;
				} catch {
					/* try the next candidate */
				}
			}
		}
		selected = computers[0]?.id ?? '';
		path = '/';
		path_input = '/';
		entries = [];
		message = 'No readable inspection root found automatically — enter a path or pick another node.';
	}

	async function browse(next: string) {
		if (!selected || loading) return;
		loading = true;
		message = 'Loading…';
		try {
			const result = await listPath(selected, next);
			path = result.path || next;
			path_input = path;
			entries = result.entries;
			message =
				result.entries.length === 0
					? 'Empty directory.'
					: `${result.entries.length} entries from ${computer?.name ?? 'node'}.`;
		} catch (e) {
			entries = [];
			message = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	function up() {
		if (path === '/' || !path.includes('/')) return void browse('/');
		const parent = path.replace(/\/+$/, '').replace(/\/[^/]*$/, '');
		void browse(parent === '' ? '/' : parent);
	}

	function open(entry: CloudFileEntry) {
		if (!(entry.is_dir ?? false)) {
			message = `${entry.name} — opening files is not wired yet.`;
			return;
		}
		const sep = path.endsWith('/') ? '' : '/';
		void browse(`${path}${sep}${entry.name}`);
	}

	function format_size(size?: number): string {
		if (size == null) return '—';
		if (size < 1024) return `${size} B`;
		if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
		if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MB`;
		return `${(size / 1024 / 1024 / 1024).toFixed(1)} GB`;
	}
</script>

<section class="finder">
	<header class="titlebar app-window-drag-handle">
		<select bind:value={selected} onchange={() => void browse('/')} aria-label="Finder computer" disabled={computers.length === 0}>
			{#if computers.length === 0}
				<option value="">No online nodes</option>
			{/if}
			{#each computers as c (c.id)}
				<option value={c.id}>{c.name}</option>
			{/each}
		</select>
		<div class="path-tools">
			<button onclick={up} disabled={!selected || path === '/'} aria-label="Parent directory">↑</button>
			<form
				class="path-form"
				onsubmit={(e) => {
					e.preventDefault();
					void browse(path_input.trim() || '/');
				}}
			>
				<input bind:value={path_input} aria-label="Directory path" spellcheck="false" />
			</form>
			<button onclick={() => void browse(path)} disabled={!selected || loading} aria-label="Refresh">⟳</button>
		</div>
	</header>

	{#if loading}
		<div class="message">Loading…</div>
	{:else if sorted.length === 0}
		<div class="message">{message || 'Empty directory.'}</div>
	{:else}
		<div class="listing" role="list">
			<div class="head" role="row">
				<span class="col-name">Name</span>
				<span class="col-size">Size</span>
				<span class="col-kind">Kind</span>
			</div>
			{#each sorted as entry (entry.name)}
				<button class="row" role="listitem" ondblclick={() => open(entry)} onclick={() => open(entry)}>
					<span class="col-name">
						<span class="glyph" class:dir={entry.is_dir ?? false}>{entry.is_dir ?? false ? '📁' : '📄'}</span>
						{entry.name}
					</span>
					<span class="col-size">{entry.is_dir ?? false ? '—' : format_size(entry.size)}</span>
					<span class="col-kind">{entry.is_dir ?? false ? 'Folder' : 'File'}</span>
				</button>
			{/each}
		</div>
	{/if}

	<footer class="statusbar">{message}</footer>
</section>

<style>
	.finder {
		height: 100%;
		display: grid;
		grid-template-rows: auto 1fr auto;

		background-color: hsla(0, 0%, 100%, 0.86);
		backdrop-filter: blur(12px);
		border-radius: inherit;
		color: #1d1d1f;
		font-size: 0.8rem;
		overflow: hidden;
	}

	.titlebar {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.35rem 0.75rem;
		background-color: hsla(0, 0%, 92%, 0.7);
		border-bottom: 0.2px solid #0002;
	}

	.titlebar select {
		background-color: #fff;
		color: #1d1d1f;
		border: 0.2px solid #0002;
		border-radius: 0.35rem;
		padding: 0.15rem 0.4rem;
		font-size: 0.75rem;
		max-width: 9rem;
	}

	.path-tools {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		flex: 1;
		min-width: 0;
	}

	.path-tools button {
		background-color: hsla(0, 0%, 92%, 0.9);
		border: 0.2px solid #0002;
		border-radius: 0.35rem;
		color: #1d1d1f;
		width: 1.4rem;
		height: 1.4rem;
		font-size: 0.75rem;
	}
	.path-tools button:disabled {
		opacity: 0.4;
	}

	.path-form {
		flex: 1;
		min-width: 0;
	}

	.path-form input {
		width: 100%;
		min-width: 0;
		background-color: #fff;
		border: 0.2px solid #0002;
		border-radius: 0.35rem;
		padding: 0.12rem 0.4rem;
		font-family: ui-monospace, Menlo, monospace;
		font-size: 0.7rem;
		color: #1d1d1f;
		outline: none;
	}
	.path-form input:focus {
		border-color: #0066cc;
	}

	.listing {
		overflow-y: auto;
		scrollbar-width: thin;
	}

	.head,
	.row {
		display: grid;
		grid-template-columns: 1fr 5rem 5rem;
		gap: 0.5rem;
		align-items: center;
		padding: 0.25rem 0.9rem;
	}

	.head {
		position: sticky;
		top: 0;
		background-color: hsla(0, 0%, 96%, 0.95);
		color: #6e6e73;
		font-size: 0.7rem;
		border-bottom: 0.2px solid #0002;
	}

	.row {
		width: 100%;
		background: transparent;
		border: none;
		text-align: left;
		color: inherit;
		font: inherit;
	}
	.row:hover {
		background-color: hsla(211, 100%, 55%, 0.12);
	}

	.col-name {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.col-size,
	.col-kind {
		color: #6e6e73;
		font-size: 0.72rem;
	}

	.message {
		display: grid;
		place-items: center;
		height: 100%;
		color: #6e6e73;
	}

	.statusbar {
		padding: 0.25rem 0.9rem;
		background-color: hsla(0, 0%, 92%, 0.7);
		border-top: 0.2px solid #0002;
		color: #6e6e73;
		font-size: 0.7rem;
	}
</style>
