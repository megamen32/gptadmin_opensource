<script lang="ts">
	import { onMount } from 'svelte';
	import {
		execCommand,
		listComputers,
		type CloudComputer,
	} from '🍎/lib/cloudos-api.ts';

	type Line = { kind: 'in' | 'out' | 'err' | 'info'; text: string };

	let computers = $state<CloudComputer[]>([]);
	let selected = $state('');
	let lines = $state<Line[]>([
		{ kind: 'info', text: 'CloudOS Terminal — every GPTAdmin node is available by default.' },
	]);
	let command = $state('');
	let running = $state(false);
	let history = $state<string[]>([]);
	let history_index = $state(-1);
	let scroll_el: HTMLElement;

	const computer = $derived(computers.find((c) => c.id === selected));

	onMount(async () => {
		try {
			computers = (await listComputers()).filter((c) => c.status === 'online');
			selected = computers[0]?.id ?? '';
		} catch {
			lines = [...lines, { kind: 'err', text: 'Hub unreachable.' }];
		}
	});

	$effect(() => {
		lines;
		scroll_el?.scrollTo({ top: scroll_el.scrollHeight });
	});

	function prompt(): string {
		return `${computer?.name ?? 'cloudos'} ~ %`;
	}

	async function run() {
		const cmd = command.trim();
		if (!cmd || running || !selected) return;
		command = '';
		history = [...history, cmd];
		history_index = -1;
		lines = [...lines, { kind: 'in', text: `${prompt()} ${cmd}` }];
		running = true;
		try {
			const out = await execCommand(selected, cmd);
			lines = [...lines, { kind: out.startsWith('⏳') || out.startsWith('HTTP') ? 'err' : 'out', text: out || '(no output)' }];
		} catch (e) {
			lines = [...lines, { kind: 'err', text: e instanceof Error ? e.message : String(e) }];
		} finally {
			running = false;
		}
	}

	function on_keydown(event: KeyboardEvent) {
		if (event.key === 'ArrowUp' && history.length > 0) {
			event.preventDefault();
			history_index = history_index < 0 ? history.length - 1 : Math.max(0, history_index - 1);
			command = history[history_index] ?? command;
		} else if (event.key === 'ArrowDown' && history_index >= 0) {
			event.preventDefault();
			history_index = history_index + 1 >= history.length ? -1 : history_index + 1;
			command = history_index < 0 ? '' : (history[history_index] ?? '');
		}
	}
</script>

<section class="terminal">
	<header class="titlebar app-window-drag-handle">
		<span class="title">Terminal</span>
		<span class="status" class:ok={!!selected}>{selected ? computer?.name : 'offline'}</span>
		<select bind:value={selected} aria-label="Terminal computer" disabled={computers.length === 0} class="computer-select">
			{#if computers.length === 0}
				<option value="">No online nodes</option>
			{/if}
			{#each computers as c (c.id)}
				<option value={c.id}>{c.name}</option>
			{/each}
		</select>
	</header>

	<div class="screen" bind:this={scroll_el}>
		{#each lines as line, i (i)}
			<p class="line {line.kind}">{line.text}</p>
		{/each}
	</div>

	<form class="input-row" onsubmit={(e) => { e.preventDefault(); void run(); }}>
		<span class="prompt">{prompt()}</span>
		<input
			bind:value={command}
			onkeydown={on_keydown}
			placeholder={selected ? '' : 'Select a node'}
			autocomplete="off"
			spellcheck="false"
			disabled={!selected || running}
		/>
		{#if running}<span class="spinner"></span>{/if}
	</form>
</section>

<style>
	.terminal {
		height: 100%;
		display: grid;
		grid-template-rows: auto 1fr auto;

		background-color: #1d1f21;
		border-radius: inherit;
		color: #e8e8e8;
		overflow: hidden;
	}

	.titlebar {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		padding: 0.35rem 0.7rem;
		background-color: hsla(0, 0%, 92%, 0.7);
		border-bottom: 0.2px solid #0002;
		color: #1d1d1f;
	}

	.title {
		font-size: 0.82rem;
		font-weight: 600;
	}

	.status {
		font-size: 0.65rem;
		color: #98989d;
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		text-align: right;
	}
	.status.ok {
		color: #1e8e3e;
	}

	.computer-select {
		background-color: #fff;
		color: #1d1d1f;
		border: 0.2px solid #0002;
		border-radius: 0.35rem;
		padding: 0.15rem 0.4rem;
		font-size: 0.72rem;
		max-width: 11rem;
		flex: none;
	}

	.screen {
		overflow-y: auto;
		padding: 0.5rem 0.75rem;
		font-family: ui-monospace, Menlo, monospace;
		font-size: 0.72rem;
		line-height: 1.4;
		scrollbar-width: thin;
	}

	.line {
		margin: 0;
		white-space: pre-wrap;
		word-break: break-word;
	}
	.line.in {
		color: #7ecbff;
	}
	.line.out {
		color: #e8e8e8;
	}
	.line.err {
		color: #ff7b72;
	}
	.line.info {
		color: #8b949e;
	}

	.input-row {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		padding: 0.4rem 0.75rem;
		background-color: #121316;
		border-top: 0.2px solid #0006;
	}

	.prompt {
		font-family: ui-monospace, Menlo, monospace;
		font-size: 0.72rem;
		color: #7ecbff;
		flex: none;
	}

	.input-row input {
		flex: 1;
		min-width: 0;
		background-color: transparent;
		border: none;
		outline: none;
		color: #e8e8e8;
		font-family: ui-monospace, Menlo, monospace;
		font-size: 0.72rem;
		caret-color: #7ecbff;
	}

	.spinner {
		width: 0.7rem;
		height: 0.7rem;
		border: 2px solid #444;
		border-top-color: #7ecbff;
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
		flex: none;
	}
	@keyframes spin {
		to { transform: rotate(360deg); }
	}
</style>
