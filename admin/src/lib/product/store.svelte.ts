import { fetchGroups, fetchPresets, refusal, type Preset } from '$lib/api/presets';
import type { Group } from './presets';

/**
 * Groups and presets as the hub has them, shared by both pages: a group
 * saved on one shows up in the other. Pages write through the API and then
 * put the hub's answer here; nothing here is a draft of its own.
 */
export const db = $state<{ groups: Group[]; presets: Preset[]; loaded: boolean; error: string }>({
	groups: [],
	presets: [],
	loaded: false,
	error: '',
});

export async function load(): Promise<void> {
	try {
		const [groups, presets] = await Promise.all([fetchGroups(), fetchPresets()]);
		db.groups = groups;
		db.presets = presets;
		db.error = '';
	} catch (e) {
		db.error = refusal(e);
	} finally {
		db.loaded = true;
	}
}

/** Puts one preset as the hub answered it back into the list. */
export function putPreset(p: Preset): void {
	const i = db.presets.findIndex((x) => x.id === p.id);
	if (i >= 0) db.presets[i] = p;
	else db.presets.push(p);
}
