import type { AdminLogEntry } from '$lib/api/logs';

// Normalize msg by stripping UUIDs and numbers so semantically identical errors
// collapse into one group. Kept intentionally small — no stack-trace parsing.
const RE_UUID = /[0-9a-f]{8}-[0-9a-f-]+/g;
const RE_NUM = /\d+/g;

export function normalizeMsg(msg: string): string {
	return msg.replace(RE_UUID, '<id>').replace(RE_NUM, '<n>');
}

export function fingerprint(cat: string, msg: string): string {
	return `${cat || '—'}::${normalizeMsg(msg)}`;
}

export interface Group {
	fingerprint: string;
	cat: string;
	samplemsg: string;
	count: number;
	latestTs: string;
	entries: AdminLogEntry[];
}

export function groupEntries(entries: AdminLogEntry[]): Group[] {
	const map = new Map<string, Group>();
	for (const e of entries) {
		const fp = fingerprint(e.cat, e.msg);
		const g = map.get(fp);
		if (g) {
			g.count++;
			g.entries.push(e);
			if (e.ts > g.latestTs) g.latestTs = e.ts;
		} else {
			map.set(fp, {
				fingerprint: fp,
				cat: e.cat || '',
				samplemsg: e.msg,
				count: 1,
				latestTs: e.ts,
				entries: [e],
			});
		}
	}
	// Sort by latestTs desc
	const arr = Array.from(map.values());
	arr.sort((a, b) => (a.latestTs < b.latestTs ? 1 : -1));
	// Cap raw entries per group to 20 (latest) to bound memory.
	for (const g of arr) {
		g.entries.sort((a, b) => (a.ts < b.ts ? 1 : -1));
		if (g.entries.length > 20) g.entries = g.entries.slice(0, 20);
	}
	return arr;
}

export function upsertEntry(groups: Group[], e: AdminLogEntry): Group[] {
	const fp = fingerprint(e.cat, e.msg);
	const idx = groups.findIndex((g) => g.fingerprint === fp);
	if (idx === -1) {
		const g: Group = {
			fingerprint: fp,
			cat: e.cat || '',
			samplemsg: e.msg,
			count: 1,
			latestTs: e.ts,
			entries: [e],
		};
		return [g, ...groups];
	}
	const g = groups[idx];
	const updated: Group = {
		...g,
		count: g.count + 1,
		latestTs: e.ts > g.latestTs ? e.ts : g.latestTs,
		entries: [e, ...g.entries].slice(0, 20),
	};
	const rest = groups.filter((_, i) => i !== idx);
	return [updated, ...rest];
}
