<script lang="ts">
	function openAdmin(): void {
		window.location.assign('/admin/');
	}
</script>

<section class="settings">
	<header class="titlebar app-window-drag-handle"><h1>GPTAdmin Settings</h1></header>
	<div class="body">
		<div class="icon" aria-hidden="true">⚙</div>
		<div>
			<h2>Control the GPTAdmin system</h2>
			<p>Nodes, access, MCP connections, security and system settings are managed in the protected GPTAdmin panel.</p>
			<button onclick={openAdmin}>Open GPTAdmin Settings</button>
			<p class="hint">Uses this administrator session; no second password or address is needed.</p>
		</div>
	</div>
</section>

<style>
	.settings { height: 100%; display: grid; grid-template-rows: auto 1fr; background: hsla(0, 0%, 100%, .9); border-radius: inherit; color: #1d1d1f; overflow: hidden; }
	.titlebar { margin: 0; padding: .55rem .9rem; background: hsla(0,0%,92%,.8); border-bottom: .2px solid #0002; font-size: .9rem; }
	.titlebar h1 { margin: 0; font-size: .9rem; }
	.body { display: flex; align-items: center; gap: 1.2rem; max-width: 34rem; margin: auto; padding: 2rem; }
	.icon { display: grid; place-items: center; flex: none; width: 4.5rem; height: 4.5rem; border-radius: 1.25rem; background: linear-gradient(145deg,#f6f7fb,#c7ccd8); color:#4b5563; font-size: 2.6rem; }
	h2 { margin: 0 0 .5rem; font-size: 1.1rem; }
	p { margin: 0; color: #6e6e73; line-height: 1.45; }
	button { margin-top: 1rem; border: 0; border-radius: .55rem; background: #0066cc; color: #fff; padding: .48rem .85rem; font: inherit; font-weight: 600; cursor: pointer; }
	button:hover, button:focus-visible { background: #005bb5; }
	.hint { margin-top: .75rem; font-size: .72rem; }
</style>
