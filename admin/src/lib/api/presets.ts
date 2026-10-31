import { get, post, put, del, ApiError, describeApiError } from './client';
import {
	categoryOf,
	groupFromService,
	rulesFromWire,
	rulesToWire,
	type CatalogService,
	type Group,
	type RuleKind,
	type WireRules,
} from '$lib/product/presets';

// Groups and presets the hub serves to clients (internal/presets/admin.go).
// Editing a group or a draft reaches no device; a release does, to the
// stage's share of buckets, and 100 makes it everyone's.

/** A release: the groups frozen as they were, with the side each was on. */
export interface Release {
	id: number;
	v: number;
	bypass: Group[];
	proxy: Group[];
	createdBy: string;
	createdAt: string;
}

export interface Preset {
	id: string;
	name: string;
	/** the preset of every device no other preset claims */
	default: boolean;
	countries: string[];
	/** the draft: group ids per side */
	bypass: string[];
	proxy: string[];
	/** what every device of the preset gets */
	current: Release | null;
	/** the release rolling out, to the buckets below stage */
	next: Release | null;
	stage: number;
	updatedAt: string;
}

interface WireGroup {
	id: string;
	name: string;
	icon: string;
	rules: WireRules;
}

interface WireRelease {
	id: number;
	v: number;
	services: CatalogService[];
	createdBy: string;
	createdAt: string;
}

type WirePreset = Omit<Preset, 'current' | 'next'> & { current: WireRelease | null; next: WireRelease | null };

function release(r: WireRelease | null): Release | null {
	if (!r) return null;
	const side = (s: 'bypass' | 'proxy') => r.services.filter((x) => x.defaultModes.includes(s)).map(groupFromService);
	return {
		id: r.id,
		v: r.v,
		bypass: side('bypass'),
		proxy: side('proxy'),
		createdBy: r.createdBy,
		createdAt: r.createdAt,
	};
}

function preset(p: WirePreset): Preset {
	return { ...p, current: release(p.current), next: release(p.next) };
}

export async function fetchGroups(): Promise<Group[]> {
	const { groups } = await get<{ groups: WireGroup[] }>('/api/v1/admin/presets/groups');
	return groups.map((g) => ({ id: g.id, name: g.name, icon: g.icon, rules: rulesFromWire(g.rules) }));
}

export function saveGroup(g: Group): Promise<unknown> {
	return put(`/api/v1/admin/presets/groups/${encodeURIComponent(g.id)}`, {
		name: g.name,
		icon: g.icon,
		rules: rulesToWire(g.rules),
	});
}

export function deleteGroup(id: string): Promise<unknown> {
	return del(`/api/v1/admin/presets/groups/${encodeURIComponent(id)}`);
}

/** The canonical form of a pasted rule, as the client will store it. */
export async function normalizeRule(kind: RuleKind, value: string): Promise<string> {
	const r = await post<{ value: string }>('/api/v1/admin/presets/rules/normalize', {
		category: categoryOf(kind),
		value,
	});
	return r.value;
}

export async function fetchPresets(): Promise<Preset[]> {
	const { presets } = await get<{ presets: WirePreset[] }>('/api/v1/admin/presets');
	return presets.map(preset);
}

type Draft = Pick<Preset, 'name' | 'default' | 'countries' | 'bypass' | 'proxy'>;

export async function createPreset(id: string, d: Draft): Promise<Preset> {
	return preset((await post<{ preset: WirePreset }>('/api/v1/admin/presets', { id, ...d })).preset);
}

export async function savePreset(id: string, d: Draft): Promise<Preset> {
	return preset((await put<{ preset: WirePreset }>(`/api/v1/admin/presets/${encodeURIComponent(id)}`, d)).preset);
}

export function deletePreset(id: string): Promise<unknown> {
	return del(`/api/v1/admin/presets/${encodeURIComponent(id)}`);
}

/** Freezes the draft into a release that rolls out at once to stage percent. */
export async function releasePreset(id: string, stage: number): Promise<Preset> {
	return preset(
		(await post<{ preset: WirePreset }>(`/api/v1/admin/presets/${encodeURIComponent(id)}/release`, { stage })).preset,
	);
}

export async function setStage(id: string, stage: number): Promise<Preset> {
	return preset(
		(await post<{ preset: WirePreset }>(`/api/v1/admin/presets/${encodeURIComponent(id)}/stage`, { stage })).preset,
	);
}

export async function revertPreset(id: string): Promise<Preset> {
	return preset((await post<{ preset: WirePreset }>(`/api/v1/admin/presets/${encodeURIComponent(id)}/revert`)).preset);
}

/** The hub's own words for a refusal: its presets errors are written for the editor. */
export function refusal(e: unknown): string {
	const text = describeApiError(e);
	return e instanceof ApiError && e.code && text === e.message ? e.code : text;
}
