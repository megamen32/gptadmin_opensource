import { apps_config } from '🍎/configs/apps/apps-config';

export type AppID = keyof typeof apps_config;

const app_ids = Object.keys(apps_config) as AppID[];

function blank_record<T>(value: () => T): Record<AppID, T> {
	return Object.fromEntries(app_ids.map((id) => [id, value()])) as Record<AppID, T>;
}

export const apps = $state({
	open: blank_record<boolean>(() => false),

	active: 'finder' satisfies AppID,

	/**
	 * Maximum zIndex for the active app
	 * Initialize with -2, so that it becomes 0 when initialised
	 */
	active_z_index: -2,

	z_indices: blank_record<number>(() => 0),

	is_being_dragged: false as boolean,

	fullscreen: blank_record<boolean>(() => false),
});
