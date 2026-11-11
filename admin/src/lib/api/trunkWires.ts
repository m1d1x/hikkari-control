import { get } from './client';
import { USE_MOCK } from '$lib/config/mock';

// The trunk's wires (stage 4, hub internal/cascadewatch chart.go): every
// call and every wire of the cascade dataset over time, one line per target,
// and the entry's selector's moves as marks on the time axis.
//
// Targets: copies1 and copies2 are the pool's own call, wire-<wire> the
// stand's process for that wire, trunk-<wire> the entry's own wire
// (trunk-pool its pool under the selector).

export type TrunkWindow = '6h' | '24h' | '7d';

export interface TrunkLine {
	/** the target */
	key: string;
	/** which chart it belongs to */
	group: string;
	/** one value per bucket; null where nothing was measured */
	values: (number | null)[];
}

export interface TrunkMark {
	at: string;
	label: string;
	class: 'voice' | 'bulk' | string;
	why: 'first' | 'red' | 'better' | string;
}

export interface TrunkWireChart {
	window: TrunkWindow;
	bucketSeconds: number;
	buckets: string[];
	lines: TrunkLine[];
	marks: TrunkMark[];
}

export async function fetchTrunkWires(window: TrunkWindow): Promise<TrunkWireChart> {
	if (USE_MOCK) return Promise.resolve(mockTrunkWires(window));
	return get<TrunkWireChart>('/api/v1/admin/trunk/wires', { window });
}

const MOCK_TARGETS = ['copies1', 'copies2', 'wire-quic', 'wire-salamander', 'wire-awg', 'wire-udp', 'trunk-udp', 'trunk-quic'];

/** A plausible window for UI work without a backend (VITE_USE_MOCK). */
function mockTrunkWires(window: TrunkWindow): TrunkWireChart {
	const bucketSeconds = window === '6h' ? 600 : window === '24h' ? 3600 : 21600;
	const n = window === '6h' ? 36 : window === '24h' ? 24 : 28;
	const end = Math.floor(Date.now() / 1000 / bucketSeconds) * bucketSeconds;
	const buckets = Array.from({ length: n }, (_, i) =>
		new Date((end - (n - 1 - i) * bucketSeconds) * 1000).toISOString(),
	);
	let seed = 11;
	const rnd = () => (seed = (seed * 16807) % 2147483647) / 2147483647;
	const lines: TrunkLine[] = [];
	const pool = (k: string) => k.startsWith('copies');
	const add = (group: string, keys: string[], f: (k: string) => number) => {
		for (const key of keys) lines.push({ key, group, values: buckets.map((_, i) => (i === 4 ? null : f(key))) });
	};
	add('call_red', MOCK_TARGETS, (k) => Math.round((pool(k) ? 2 : 0.2) * rnd() * 3));
	add('call_gaps60', MOCK_TARGETS, (k) => Math.round((pool(k) ? 8 : 2) * rnd() * 3));
	add('call_rtt', MOCK_TARGETS, (k) => (pool(k) ? 60 : 48) + rnd() * 30);
	add('call_gap', MOCK_TARGETS, (k) => (pool(k) ? 400 + rnd() * 3000 : 60 + rnd() * 200));
	add('call_loss', MOCK_TARGETS, (k) => rnd() * (pool(k) ? 6 : 1));
	add('call_silent', MOCK_TARGETS, (k) => rnd() * (pool(k) ? 6 : 1));
	const wires = MOCK_TARGETS.filter((k) => !pool(k));
	add('bulk_mbps', wires, (k) => (k.endsWith('awg') ? 90 : k.endsWith('udp') ? 20 : 150) + rnd() * 80);
	add('bulk_failed', wires, () => (rnd() > 0.9 ? 1 : 0));
	add('cpu', wires.filter((k) => k.startsWith('wire-')), (k) => (k.endsWith('udp') ? 2 : 3.5) + rnd());
	const marks: TrunkMark[] = [
		{ at: buckets[n - 6], label: 'голос: udp красный, на pool', class: 'voice', why: 'red' },
		{ at: buckets[n - 3], label: 'голос: pool → udp, лучше 3m0s', class: 'voice', why: 'better' },
	];
	return { window, bucketSeconds, buckets, lines, marks };
}
