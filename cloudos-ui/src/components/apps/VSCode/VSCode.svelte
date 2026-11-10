<script lang="ts">
	import { onMount } from 'svelte';
	import {
		listComputers,
		readRemoteFile,
		writeRemoteFile,
		type CloudComputer,
	} from '🍎/lib/cloudos-api.ts';

	const {
		is_being_dragged,
	}: {
		is_being_dragged: boolean;
	} = $props();

	let computers = $state<CloudComputer[]>([]);
	let selected = $state('');
	let path_input = $state('');
	let text = $state('');
	let loaded_path = $state('');
	let loaded_sha = $state('');
	let dirty = $state(false);
	let message = $state('');
	let error = $state('');
	let working = $state(false);
	let textarea_el: HTMLTextAreaElement;

	const computer = $derived(computers.find((c) => c.id === selected));
	const editable = $derived(!!computer?.file_target || !!selected);

	onMount(async () => {
		try {
			computers = (await listComputers()).filter(
				(c) => c.status === 'online' && (c.file_target || c.capabilities.includes('file_transfer')),
			);
			selected = computers[0]?.id ?? '';
		} catch {
			error = 'Hub unreachable.';
		}
	});

	async function open_file() {
		if (!selected || working) return;
		const path = path_input.trim();
		if (!path) {
			error = 'Enter a file path first.';
			return;
		}
		error = '';
		message = 'Opening…';
		working = true;
		try {
			const file = await readRemoteFile(selected, path);
			text = file.text;
			loaded_path = file.path;
			loaded_sha = file.sha256;
			path_input = file.path;
			dirty = false;
			message = `${file.path} · ${file.size} bytes`;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
			message = '';
		} finally {
			working = false;
		}
	}

	async function save_file() {
		if (!selected || working) return;
		if (!loaded_path) {
			error = 'Open a file first.';
			return;
		}
		error = '';
		message = 'Saving…';
		working = true;
		try {
			const result = await writeRemoteFile(selected, loaded_path, text, loaded_sha);
			loaded_sha = result.sha256;
			dirty = false;
			message = `Saved ${result.path}${result.created ? ' (new file)' : ''} · ${result.sha256.slice(0, 12)}…`;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			working = false;
		}
	}

	function on_input() {
		dirty = true;
	}

	function on_keydown(event: KeyboardEvent) {
		if ((event.metaKey || event.ctrlKey) && event.key === 's') {
			event.preventDefault();
			void save_file();
		}
	}
</script>

<section class="editor" class:dragging={is_being_dragged}>
	<header class="titlebar app-window-drag-handle">
		<span class="title">Editor</span>
		<button class="tb" onclick={() => void open_file()} disabled={!selected || working}>Open</button>
		<button class="tb save" class:dirty onclick={() => void save_file()} disabled={!selected || working || !loaded_path}>
			Save{dirty ? ' •' : ''}
		</button>
		<span class="path-wrap">
			<input
				bind:value={path_input}
				placeholder="/home/admin/project/src/main.go"
				spellcheck="false"
				autocapitalize="off"
				onkeydown={(e) => {
					if (e.key === 'Enter') void open_file();
				}}
			/>
		</span>
		<select bind:value={selected} aria-label="Editor computer" disabled={computers.length === 0} class="computer-select">
			{#if computers.length === 0}
				<option value="">No online nodes</option>
			{/if}
			{#each computers as c (c.id)}
				<option value={c.id}>{c.name}</option>
			{/each}
		</select>
	</header>

	{#if error}
		<p class="error">{error}</p>
	{/if}

	<textarea
		bind:this={textarea_el}
		bind:value={text}
		oninput={on_input}
		onkeydown={on_keydown}
		placeholder={selected ? 'Open a file, or paste text and Save to write it to the node…' : 'Pick a node with file transfer…'}
		spellcheck="false"
	></textarea>

	<footer class="statusbar">
		<span class="msg">{message || (loaded_path ? loaded_path : '⌘S saves')}</span>
		<span class="sha">{loaded_sha ? loaded_sha.slice(0, 16) : ''}</span>
	</footer>
</section>

<style>
	.editor {
		height: 100%;
		display: grid;
		grid-template-rows: auto auto 1fr auto;

		background-color: #1e2126;
		border-radius: inherit;
		color: #e8e8e8;
		font-size: 0.8rem;
		overflow: hidden;
	}

	.titlebar {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		padding: 0.35rem 0.6rem;
		background-color: hsla(0, 0%, 92%, 0.7);
		border-bottom: 0.2px solid #0002;
		color: #1d1d1f;
	}

	.title {
		font-size: 0.82rem;
		font-weight: 600;
		flex: none;
	}

	.tb {
		background-color: #e8e8ed;
		border: 0.2px solid #0002;
		border-radius: 0.4rem;
		padding: 0.15rem 0.6rem;
		font-size: 0.72rem;
		flex: none;
	}
	.tb.save {
		background-color: #0066cc;
		border-color: #0066cc;
		color: #fff;
	}
	.tb.save.dirty {
		background-color: #ff9500;
		border-color: #ff9500;
	}
	.tb:disabled {
		opacity: 0.4;
	}

	.path-wrap {
		flex: 1;
		min-width: 0;
	}

	.path-wrap input {
		width: 100%;
		min-width: 0;
		background-color: #fff;
		border: 0.2px solid #0002;
		border-radius: 0.45rem;
		padding: 0.15rem 0.5rem;
		font-family: ui-monospace, Menlo, monospace;
		font-size: 0.7rem;
		outline: none;
	}
	.path-wrap input:focus {
		border-color: #0066cc;
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

	.error {
		margin: 0;
		padding: 0.2rem 0.6rem;
		background-color: #3a1518;
		color: #ff7b72;
		font-size: 0.7rem;
	}

	textarea {
		width: 100%;
		height: 100%;
		box-sizing: border-box;
		resize: none;
		border: none;
		outline: none;
		background-color: #1e2126;
		color: #e8e8e8;
		font-family: ui-monospace, Menlo, monospace;
		font-size: 0.75rem;
		line-height: 1.5;
		padding: 0.6rem 0.8rem;
		tab-size: 4;
		scrollbar-width: thin;
	}

	.statusbar {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.22rem 0.6rem;
		background-color: #121316;
		border-top: 0.2px solid #0006;
		color: #8b949e;
		font-size: 0.65rem;
	}

	.msg {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.sha {
		font-family: ui-monospace, Menlo, monospace;
		flex: none;
	}
</style>
