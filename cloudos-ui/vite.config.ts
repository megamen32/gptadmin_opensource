import { svelte } from '@sveltejs/vite-plugin-svelte';
import UnpluginIcons from 'unplugin-icons/vite';
import { defineConfig } from 'vite';
import { imagetools } from 'vite-imagetools';

import { prefetch } from './prefetch-plugin';

export default defineConfig({
	// CloudOS is served by the GPTAdmin hub below /cloudos/ on the same origin.
	base: '/cloudos/',

	plugins: [
		svelte(),
		prefetch(),

		UnpluginIcons({ autoInstall: true, compiler: 'svelte' }),
		imagetools(),
	],

	resolve: {
		alias: {
			'🍎': new URL('./src/', import.meta.url).pathname,
		},
	},
	// Vite 8 (Rolldown) minifies JS with its built-in minifier and CSS with
	// lightningcss by default — no explicit build/css config needed.
});
