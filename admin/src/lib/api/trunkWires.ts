import { get } from './client';
import { USE_MOCK } from '$lib/config/mock';

// The trunk's wires (stage 4, hub internal/cascadewatch chart.go): every
// call and every wire of the cascade dataset over time, one line per target,
// and the entry's selector's moves as marks on the time axis.
//
// Targets: copies1 and copies2 are the pool's own call, wire-<wire> the
// stand's process for that wire, trunk-<wire> the entry's own wire
// (trunk-pool its pool under the selector).

export type TrunkWindow = '6h' | '24h' | '7d' | '30d';

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

// The fleet's own targets: the names the entry and the stand publish — the
// pool's call of the two fates, the entry's wires, the stand's process per
// wire and the stand over hk-pipe.
const MOCK_TARGETS = ['copies1', 'copies2', 'trunk-udp', 'trunk-h3dgram', 'trunk-pool', 'pipe-copies1', 'pipe-copies2', 'pipe-udp', 'wire-udp', 'wire-pool'];

/** A plausible window for UI work without a backend (VITE_USE_MOCK). */
function mockTrunkWires(window: TrunkWindow): TrunkWireChart {
	const bucketSeconds = window === '6h' ? 60 : window === '24h' ? 300 : window === '7d' ? 1800 : 3600;
	const n = window === '6h' ? 360 : window === '24h' ? 288 : window === '7d' ? 336 : 720;
	const end = Math.floor(Date.now() / 1000 / bucketSeconds) * bucketSeconds;
	const buckets = Array.from({ length: n }, (_, i) =>
		new Date((end - (n - 1 - i) * bucketSeconds) * 1000).toISOString(),
	);
	let seed = 11;
	const rnd = () => (seed = (seed * 16807) % 2147483647) / 2147483647;
	const lines: TrunkLine[] = [];
	const pool = (k: string) => k.startsWith('copies') || k.startsWith('pipe-copies');
	// The shapes are the ones the fleet shows today: the pool's route holds,
	// a single wire takes the entry's wave on :x0.
	const add = (group: string, keys: string[], f: (k: string, i: number) => number) => {
		for (const key of keys) lines.push({ key, group, values: buckets.map((_, i) => (i === 4 ? null : f(key, i))) });
	};
	add('call_red', MOCK_TARGETS, (k, i) => (pool(k) ? (i === n - 9 ? 1 : 0) : rnd() > 0.75 ? 1 : 0));
	add('call_gaps60', MOCK_TARGETS, (k) => Math.round((pool(k) ? 1 : 3) * rnd()));
	add('call_rtt', MOCK_TARGETS, (k) => (pool(k) ? 52 : 48) + rnd() * 14);
	add('call_rtt50', MOCK_TARGETS, (k) => (pool(k) ? 47 : 43) + rnd() * 9);
	// the pool's deaths: the fabric between the entry and the core, per entry
	add('pool_dead_stalled', ['aezamsk1'], () => 190 + rnd() * 130);
	add('pool_dead_broken', ['aezamsk1'], () => 50 + rnd() * 40);
	add('call_gap', MOCK_TARGETS, (k, i) => (pool(k) ? (i === n - 7 ? 280 : i === n - 15 ? 190 : 29 + rnd() * 62) : 38 + rnd() * 240));
	add('call_loss', MOCK_TARGETS, (k) => (pool(k) ? rnd() * 2 : rnd() * 9));
	add('call_silent', MOCK_TARGETS, (k) => (pool(k) ? (rnd() > 0.9 ? rnd() * 0.07 : 0) : rnd() * 1.7));
	const wires = MOCK_TARGETS.filter((k) => k.startsWith('wire-'));
	add('bulk_mbps', wires, (k) => (k.endsWith('pool') ? 90 : 150) + rnd() * 80);
	add('bulk_failed', wires, () => (rnd() > 0.9 ? 1 : 0));
	add('cpu', wires, () => 2 + rnd());
	// the product's numbers: what a person gets, what it costs him, who is on it
	add('bulk_speed_p50', ['aezamsk1'], () => 150 + rnd() * 80);
	add('bulk_speed_p10', ['aezamsk1'], () => 40 + rnd() * 50);
	add('cascade_price', ['aezamsk1'], () => 10 + rnd() * 8);
	add('users_hikkari', ['aezamsk1'], () => Math.round(2 + rnd() * 2));
	add('users_naive', ['aezamsk1'], () => Math.round(rnd() * 2));
	add('users_reality', ['aezamsk1'], () => Math.round(1 + rnd()));
	const marks: TrunkMark[] = [
		{ at: buckets[n - 9], label: 'голос: udp красный, на h3dgram', class: 'voice', why: 'red' },
		{ at: buckets[n - 4], label: 'голос: h3dgram → udp, лучше 5m0s', class: 'voice', why: 'better' },
	];
	return { window, bucketSeconds, buckets, lines, marks };
}
