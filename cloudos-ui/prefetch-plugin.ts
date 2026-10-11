import type { HtmlTagDescriptor, Plugin } from 'vite';

export function prefetch(): Plugin {
	let base = '/';

	return {
		name: 'prefetch',

		enforce: 'post',
		apply: 'build',

		configResolved(config) {
			base = config.base ?? '/';
		},

		transformIndexHtml: (html, ctx) => {
			const tags = Object.keys(ctx.bundle)
				.filter((v) => !v.toString().endsWith('webp'))
				.map(
					(chunkName) =>
						({
							injectTo: 'head',
							tag: 'link',
							attrs: {
								rel: 'prefetch',
								href: `${base}${chunkName}`,
							},
						}) as HtmlTagDescriptor,
				);

			return {
				html,
				tags,
			};
		},
	};
}
