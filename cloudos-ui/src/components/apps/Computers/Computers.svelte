<script lang="ts">
	import { onMount } from 'svelte';
	import {
		listComputers,
		type CloudComputer,
	} from '🍎/lib/cloudos-api.ts';

	let computers = $state<CloudComputer[]>([]);
	let error = $state('');
	let loading = $state(true);

	onMount(async () => {
		try {
			computers = await listComputers();
		} catch {
			error = 'Hub unreachable.';
		} finally {
			loading = false;
		}
	});

	function seen(computer: CloudComputer): string {
		if (typeof computer.last_seen === 'number' && computer.last_seen > 0) {
			return new Date(computer.last_seen * 1000).toLocaleString();
		}
		if (typeof computer.last_seen === 'string' && computer.last_seen) return computer.last_seen;
		return '—';
	}

	const online = $derived(computers.filter((c) => c.status === 'online').length);
</script>

<section class="computers">
	<header class="titlebar app-window-drag-handle">
		<h1>Computers</h1>
		<span class="count">{computers.length} total · {online} online</span>
	</header>

	<div class="body">
		{#if loading}
			<p class="hint">Loading fleet…</p>
		{:else if error}
			<p class="hint error">{error}</p>
		{:else if computers.length === 0}
			<p class="hint">No computers found. Every GPTAdmin node appears here automatically once its shell executor connects to the hub.</p>
		{:else}
			{#each computers as computer (computer.id)}
				<article class="computer" class:offline={computer.status !== 'online'}>
					<div class="row-1">
						<span class="dot" class:on={computer.status === 'online'}></span>
						<span class="name">{computer.name}</span>
						<span class="kind" class:node={computer.kind === 'node'}>{computer.kind === 'node' ? 'node' : 'agent'}</span>
					</div>
					<div class="row-2">
						<span class="os">{computer.os || 'unknown os'}</span>
					</div>
					<div class="row-3">
						{#each computer.capabilities as cap (cap)}
							<span class="chip">{cap}</span>
						{/each}
						<span class="seen">seen {seen(computer)}</span>
					</div>
				</article>
			{/each}
		{/if}
	</div>

	<footer class="statusbar">
		Every unified GPTAdmin node is part of CloudOS by default — no pairing or opt-in required.
	</footer>
</section>

<style>
	.computers {
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
		align-items: baseline;
		justify-content: space-between;
		padding: 0.45rem 0.9rem;
		background-color: hsla(0, 0%, 92%, 0.7);
		border-bottom: 0.2px solid #0002;
	}
	.titlebar h1 {
		margin: 0;
		font-size: 0.9rem;
		font-weight: 600;
	}
	.count {
		color: #6e6e73;
		font-size: 0.7rem;
	}

	.body {
		overflow-y: auto;
		padding: 0.6rem 0.9rem;
		display: grid;
		gap: 0.5rem;
		align-content: start;
		scrollbar-width: thin;
	}

	.hint {
		color: #6e6e73;
		text-align: center;
		margin-top: 2rem;
	}
	.hint.error {
		color: #d70015;
	}

	.computer {
		background-color: hsla(0, 0%, 100%, 0.75);
		border: 0.2px solid #0002;
		border-radius: 0.55rem;
		padding: 0.45rem 0.6rem;
	}
	.computer.offline {
		opacity: 0.55;
	}

	.row-1 {
		display: flex;
		align-items: center;
		gap: 0.45rem;
	}
	.name {
		font-weight: 600;
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.dot {
		width: 0.55rem;
		height: 0.55rem;
		border-radius: 50%;
		background-color: #98989d;
		flex: none;
	}
	.dot.on {
		background-color: #30d158;
	}
	.kind {
		font-size: 0.62rem;
		color: #6e6e73;
		border: 0.2px solid #0002;
		padding: 0 0.3rem;
		border-radius: 0.3rem;
		text-transform: uppercase;
	}
	.kind.node {
		color: #0066cc;
		border-color: #0066cc44;
	}

	.row-2 {
		margin-top: 0.15rem;
	}
	.os {
		color: #6e6e73;
		font-size: 0.72rem;
	}

	.row-3 {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		margin-top: 0.3rem;
		flex-wrap: wrap;
	}
	.chip {
		background-color: hsla(211, 100%, 55%, 0.12);
		color: #0066cc;
		border-radius: 0.3rem;
		font-size: 0.62rem;
		padding: 0 0.3rem;
	}
	.seen {
		color: #98989d;
		font-size: 0.62rem;
		margin-left: auto;
	}

	.statusbar {
		padding: 0.25rem 0.9rem;
		background-color: hsla(0, 0%, 92%, 0.7);
		border-top: 0.2px solid #0002;
		color: #6e6e73;
		font-size: 0.7rem;
	}
</style>
