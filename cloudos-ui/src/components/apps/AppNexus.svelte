<script lang="ts">
	import type { AppID } from '🍎/state/apps.svelte';

	const {
		app_id,
		is_being_dragged,
	}: {
		app_id: AppID;
		is_being_dragged: boolean;
	} = $props();
</script>

{#if app_id === 'finder'}
	{#await import('./Finder/Finder.svelte') then { default: Finder }}
		<Finder />
	{/await}
{:else if app_id === 'terminal'}
	{#await import('./Terminal/Terminal.svelte') then { default: Terminal }}
		<Terminal />
	{/await}
{:else if app_id === 'browser'}
	{#await import('./Browser/Browser.svelte') then { default: Browser }}
		<Browser />
	{/await}
{:else if app_id === 'computers'}
	{#await import('./Computers/Computers.svelte') then { default: Computers }}
		<Computers />
	{/await}
{:else if app_id === 'calendar'}
	{#await import('./Calendar/Calendar.svelte') then { default: Calendar }}
		<Calendar />
	{/await}
{:else if app_id === 'vscode'}
	{#await import('./VSCode/VSCode.svelte') then { default: VSCode }}
		<VSCode {is_being_dragged} />
	{/await}
{:else if app_id === 'calculator'}
	{#await import('./Calculator/Calculator.svelte') then { default: Calculator }}
		<Calculator />
	{/await}
{:else if app_id === 'wallpapers'}
	{#await import('./WallpaperApp/WallpaperSelectorApp.svelte') then { default: WallpaperSelector }}
		<WallpaperSelector />
	{/await}
{/if}
