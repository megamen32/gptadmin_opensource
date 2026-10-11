<script lang="ts">
	import { onMount } from 'svelte';
	import {
		listBrowserConnectors,
		loadBrowserTabs,
		type BrowserConnector,
	} from '🍎/lib/cloudos-api.ts';

	let connectors = $state<BrowserConnector[]>([]);
	let connector = $state('');
	let tabs = $state('');
	let error = $state('');
	let loading = $state(false);

	onMount(async () => {
		try {
			connectors = (await listBrowserConnectors()).filter((c) => c.status !== 'failed');
		} catch {
			error = 'Browser connectors unavailable.';
		}
	});

	async function load() {
		if (!connector || loading) return;
		error = '';
		tabs = '';
		loading = true;
		try {
			tabs = await loadBrowserTabs(connector);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}
</script>

<section class="browser">
	<header class="titlebar app-window-drag-handle">
		<h1>Browser</h1>
		<span class="sub">Chrome tabs through BrowserClaw/CDP on a connected machine</span>
	</header>

	<div class="toolbar">
		<select bind:value={connector} aria-label="Browser connector">
			<option value="">Choose browser…</option>
			{#each connectors as c (c.id)}
				<option value={c.id}>{c.name} ({c.parent.replace('shell:', '')})</option>
			{/each}
		</select>
		<button onclick={() => void load()} disabled={!connector || loading}>
			{loading ? 'Loading…' : 'Load Chrome tabs'}
		</button>
	</div>

	{#if error}
		<p class="error">{error}</p>
	{/if}

	{#if tabs}
		<pre class="tabs">{tabs}</pre>
	{/if}
</section>

<style>
	.browser {
		height: 100%;
		display: grid;
		grid-template-rows: auto auto 1fr;
		grid-template-areas:
			'titlebar'
			'toolbar'
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
		align-items: baseline;
		gap: 0.6rem;
		padding: 0.45rem 0.9rem;
		background-color: hsla(0, 0%, 92%, 0.7);
		border-bottom: 0.2px solid #0002;
	}
	.titlebar h1 {
		margin: 0;
		font-size: 0.9rem;
		font-weight: 600;
	}
	.sub {
		color: #6e6e73;
		font-size: 0.7rem;
	}

	.toolbar {
		grid-area: toolbar;
		display: flex;
		gap: 0.5rem;
		padding: 0.5rem 0.9rem;
	}

	.toolbar select {
		flex: 1;
		min-width: 0;
		background-color: #fff;
		border: 0.2px solid #0002;
		border-radius: 0.35rem;
		padding: 0.2rem 0.4rem;
		font-size: 0.75rem;
	}

	.toolbar button {
		background-color: #0066cc;
		border: none;
		border-radius: 0.45rem;
		color: #fff;
		padding: 0.25rem 0.8rem;
		font-size: 0.75rem;
	}
	.toolbar button:disabled {
		opacity: 0.4;
	}

	.error {
		grid-area: content;
		color: #d70015;
		padding: 0 0.9rem;
		margin: 0;
	}

	.tabs {
		grid-area: content;
		overflow: auto;
		margin: 0;
		padding: 0.6rem 0.9rem;
		background-color: #1d1f21;
		color: #e8e8e8;
		border-radius: 0.55rem;
		margin: 0 0.9rem 0.6rem;
		font-size: 0.7rem;
		line-height: 1.35;
		white-space: pre-wrap;
		scrollbar-width: thin;
	}
</style>
