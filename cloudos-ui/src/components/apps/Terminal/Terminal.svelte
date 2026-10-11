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
		return `${computer?.name ?? 'cloudos'} ${'~'} %`;
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
		<select bind:value={selected} aria-label="Terminal computer" disabled={computers.length === 0}>
			{#if computers.length === 0}
				<option value="">No online nodes</option>
			{/if}
			{#each computers as c (c.id)}
				<option value={c.id}>{c.name}</option>
			{/each}
		</select>
		<span class="status" class:ok={!!selected}>{selected ? 'online' : 'offline'}</span>
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

		background-color: #1d1f21ee;
		border-radius: inherit;
		color: #e8e8e8;
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 0.8rem;
	}

	.titlebar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.4rem 0.75rem;

		background-color: #3a3a3cd0;
		backdrop-filter: blur(10px);
		border-bottom: 0.2px solid #0006;
	}

	.titlebar select {
		background-color: #2c2c2e;
		color: #e8e8e8;
		border: 0.2px solid #ffffff2e;
		border-radius: 0.35rem;
		padding: 0.15rem 0.4rem;
		font-size: 0.75rem;
		min-width: 0;
	}

	.status {
		font-size: 0.65rem;
		color: #98989d;
	}
	.status.ok {
		color: #30d158;
	}

	.screen {
		overflow-y: auto;
		padding: 0.6rem 0.75rem;
		scrollbar-width: thin;
		scrollbar-color: #ffffff33 transparent;
	}

	.line {
		margin: 0 0 0.15rem;
		white-space: pre-wrap;
		word-break: break-word;
	}
	.line.in {
		color: #7ee787;
	}
	.line.err {
		color: #ff6961;
	}
	.line.info {
		color: #98989d;
	}

	.input-row {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		padding: 0.4rem 0.75rem 0.55rem;
	}

	.prompt {
		color: #7ee787;
		white-space: nowrap;
	}

	.input-row input {
		flex: 1;
		min-width: 0;
		background: transparent;
		border: none;
		outline: none;
		color: #e8e8e8;
		font: inherit;
		caret-color: #7ee787;
	}
	.input-row input:disabled {
		opacity: 0.5;
	}

	.spinner {
		width: 0.7rem;
		height: 0.7rem;
		border-radius: 50%;
		border: 2px solid #ffffff33;
		border-top-color: #7ee787;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
