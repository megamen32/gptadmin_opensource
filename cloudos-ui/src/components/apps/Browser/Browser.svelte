<script lang="ts">
	import { onMount } from 'svelte';
	import {
		listBrowserConnectors,
		browserNavigate,
		browserScreenshot,
		browserSnapshot,
		browserClick,
		type BrowserConnector,
	} from '🍎/lib/cloudos-api.ts';

	let connectors = $state<BrowserConnector[]>([]);
	let connector = $state('');
	let url_input = $state('https://example.com');
	let frame = $state(''); // data: URL of the current screenshot
	let frame_title = $state('');
	let frame_url = $state('');
	let snapshot = $state('');
	let error = $state('');
	let loading = $state(false);
	let streaming = $state(false);
	let show_snapshot = $state(false);
	let stream_timer: ReturnType<typeof setInterval> | undefined;
	let busy = $state(false); // guards overlapping polls

	onMount(async () => {
		try {
			connectors = (await listBrowserConnectors()).filter((c) => c.status === 'online');
			connector = connectors[0]?.id ?? '';
		} catch {
			error = 'Browser connectors unavailable.';
		}
	});

	$effect(() => {
		return () => {
			if (stream_timer) clearInterval(stream_timer);
		};
	});

	async function poll_once() {
		if (!connector || busy) return;
		busy = true;
		try {
			const shot = await browserScreenshot(connector);
			frame = shot.image;
			frame_title = shot.title;
			frame_url = shot.url;
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}

	function toggle_stream() {
		if (streaming) {
			streaming = false;
			if (stream_timer) clearInterval(stream_timer);
			stream_timer = undefined;
			return;
		}
		streaming = true;
		void poll_once();
		stream_timer = setInterval(() => void poll_once(), 2000);
	}

	async function go() {
		if (!connector || loading) return;
		error = '';
		loading = true;
		try {
			let url = url_input.trim();
			if (url && !/^https?:\/\//i.test(url)) url = 'https://' + url;
			await browserNavigate(connector, url);
			await poll_once();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	async function refresh_snapshot() {
		if (!connector || loading) return;
		error = '';
		loading = true;
		try {
			snapshot = await browserSnapshot(connector);
			show_snapshot = true;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	async function click_ref(ref: string) {
		if (!connector) return;
		error = '';
		try {
			await browserClick(connector, ref);
			await poll_once();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	/** Parse "- link "Learn more" [ref=e1]" lines into clickable chips. */
	const ref_links = $derived(
		[...snapshot.matchAll(/\[ref=(e\d+)\]/g)].slice(0, 24).map((m) => m[1]),
	);
</script>

<section class="browser">
	<header class="titlebar app-window-drag-handle">
		<div class="nav">
			<button onclick={() => void go()} disabled={!connector || loading} aria-label="Reload">⟳</button>
		</div>
		<form
			class="url-form"
			onsubmit={(e) => {
				e.preventDefault();
				void go();
			}}
		>
			<input
				bind:value={url_input}
				placeholder={connector ? 'Enter URL and press Enter' : 'Pick a browser…'}
				spellcheck="false"
				autocapitalize="off"
			/>
		</form>
		<select bind:value={connector} aria-label="Browser connector" class="connector">
			<option value="">Browser…</option>
			{#each connectors as c (c.id)}
				<option value={c.id}>{c.parent.replace('shell:', '')}</option>
			{/each}
		</select>
	</header>

	<div class="toolbar">
		<button
			class:active={streaming}
			onclick={toggle_stream}
			disabled={!connector}
			title="Live screenshot stream every 2s"
		>
			{streaming ? '⏸ Stop stream' : '▶ Live stream'}
		</button>
		<button onclick={() => void poll_once()} disabled={!connector || busy}>📷 Snapshot</button>
		<button onclick={() => void refresh_snapshot()} disabled={!connector || loading}>🔤 Text (a11y)</button>
		{#if show_snapshot}
			<button onclick={() => (show_snapshot = false)}>🖥 View</button>
		{/if}
		<span class="live" class:on={streaming}></span>
		<span class="status-text">{frame_title || frame_url || (streaming ? 'streaming…' : 'idle')}</span>
	</div>

	{#if error}
		<p class="error">{error}</p>
	{/if}

	{#if show_snapshot}
		<div class="a11y">
			{#if ref_links.length > 0}
				<div class="refs">
					{#each ref_links as ref (ref)}
						<button onclick={() => void click_ref(ref)}>{ref}</button>
					{/each}
				</div>
			{/if}
			<pre>{snapshot}</pre>
		</div>
	{:else if frame}
		<div class="viewport">
			<img src={frame} alt={frame_title || 'browser screenshot'} draggable="false" />
		</div>
	{:else if !error}
		<div class="placeholder">
			<p>Pick a browser, type a URL and press Enter.</p>
			<p class="sub">Live stream sends a fresh JPEG screenshot every 2 seconds through the node's Chrome.</p>
		</div>
	{/if}
</section>

<style>
	.browser {
		height: 100%;
		display: grid;
		grid-template-rows: auto auto auto 1fr;
		grid-template-areas:
			'titlebar'
			'toolbar'
			'error'
			'content';

		background-color: hsla(0, 0%, 100%, 0.86);
		backdrop-filter: blur(12px);
		border-radius: inherit;
		color: #1d1d1f;
		font-size: 0.8rem;
		overflow: hidden;
	}

	.titlebar {
		grid-area: titlebar;
		display: flex;
		align-items: center;
		gap: 0.4rem;
		padding: 0.35rem 0.6rem;
		background-color: hsla(0, 0%, 92%, 0.7);
		border-bottom: 0.2px solid #0002;
	}

	.nav button {
		background-color: hsla(0, 0%, 92%, 0.9);
		border: 0.2px solid #0002;
		border-radius: 0.35rem;
		width: 1.5rem;
		height: 1.5rem;
		font-size: 0.8rem;
		flex: none;
	}

	.url-form {
		flex: 1;
		min-width: 0;
	}

	.url-form input {
		width: 100%;
		min-width: 0;
		background-color: #fff;
		border: 0.2px solid #0002;
		border-radius: 0.7rem;
		padding: 0.16rem 0.6rem;
		font-size: 0.75rem;
		outline: none;
	}
	.url-form input:focus {
		border-color: #0066cc;
	}

	.connector {
		background-color: #fff;
		border: 0.2px solid #0002;
		border-radius: 0.35rem;
		padding: 0.15rem 0.35rem;
		font-size: 0.72rem;
		max-width: 10rem;
		flex: none;
	}

	.toolbar {
		grid-area: toolbar;
		display: flex;
		align-items: center;
		gap: 0.4rem;
		padding: 0.35rem 0.6rem;
		border-bottom: 0.2px solid #0001;
	}

	.toolbar button {
		background-color: #e8e8ed;
		border: 0.2px solid #0002;
		border-radius: 0.45rem;
		color: #1d1d1f;
		padding: 0.18rem 0.6rem;
		font-size: 0.72rem;
	}
	.toolbar button.active {
		background-color: #0066cc;
		color: #fff;
		border-color: #0066cc;
	}
	.toolbar button:disabled {
		opacity: 0.4;
	}

	.live {
		width: 0.5rem;
		height: 0.5rem;
		border-radius: 50%;
		background-color: #98989d;
		flex: none;
	}
	.live.on {
		background-color: #30d158;
		animation: pulse 1.6s infinite;
	}
	@keyframes pulse {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.35; }
	}

	.status-text {
		color: #6e6e73;
		font-size: 0.68rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.error {
		grid-area: error;
		color: #d70015;
		padding: 0.15rem 0.6rem;
		margin: 0;
		font-size: 0.7rem;
	}

	.viewport {
		grid-area: content;
		overflow: auto;
		display: grid;
		place-items: start center;
		padding: 0.5rem;
		background-color: #1d1f21;
	}
	.viewport img {
		max-width: 100%;
		height: auto;
		border-radius: 0.4rem;
		box-shadow: 0 4px 18px #0008;
		user-select: none;
	}

	.placeholder {
		grid-area: content;
		display: grid;
		place-content: center;
		text-align: center;
		color: #6e6e73;
		gap: 0.4rem;
	}
	.placeholder .sub {
		font-size: 0.7rem;
		max-width: 24rem;
	}

	.a11y {
		grid-area: content;
		overflow: auto;
		padding: 0.5rem 0.6rem;
		background-color: #1d1f21;
		color: #e8e8e8;
	}
	.a11y pre {
		margin: 0;
		font-size: 0.68rem;
		line-height: 1.4;
		white-space: pre-wrap;
	}
	.refs {
		display: flex;
		gap: 0.25rem;
		flex-wrap: wrap;
		margin-bottom: 0.5rem;
	}
	.refs button {
		background-color: #2c2f33;
		border: 0.2px solid #444;
		border-radius: 0.3rem;
		color: #8ab4f8;
		font-size: 0.65rem;
		padding: 0.05rem 0.35rem;
	}
	.refs button:hover {
		background-color: #3a3d42;
	}
</style>
