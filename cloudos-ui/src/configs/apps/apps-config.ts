import { create_app_config } from '🍎/helpers/create-app-config.ts';

const finder = create_app_config({
	title: 'Finder',
	resizable: true,

	width: 900,
	height: 560,
});

const terminal = create_app_config({
	title: 'Terminal',
	resizable: true,

	width: 680,
	height: 420,
});

const browser = create_app_config({
	title: 'Browser',
	resizable: true,

	width: 900,
	height: 600,
});

const computers = create_app_config({
	title: 'Computers',
	resizable: true,

	width: 640,
	height: 520,
});

const wallpapers = create_app_config({
	title: 'Wallpapers',
	resizable: true,

	height: 600,
	width: 800,

	dock_breaks_before: true,
});

const calculator = create_app_config({
	title: 'Calculator',

	expandable: true,
	resizable: false,

	height: 250 * 1.414,
	width: 250,
});

const calendar = create_app_config({
	title: 'Calendar',
	resizable: true,
});

const vscode = create_app_config({
	title: 'Editor',
	resizable: true,

	height: 600,
	width: 900,
});

export const apps_config = {
	finder,
	terminal,
	browser,
	computers,

	wallpapers,
	calculator,
	calendar,
	vscode,
};
