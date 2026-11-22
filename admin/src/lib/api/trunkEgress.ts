import { get } from './client';
import { USE_MOCK } from '$lib/config/mock';
import type { TrunkWindow, TrunkWireChart } from './trunkWires';

// The receivers' watches of their egresses (H4, hub internal/cascadewatch
// egress.go): per receiver and egress the seconds without an answer through
// the tunnel, the seconds the internet was silent through the egress that
// carried, the round trip, and who carried; the watch's moves as marks.
// Keys are "<receiver> → <egress>".

export async function fetchTrunkEgress(window: TrunkWindow): Promise<TrunkWireChart> {
	if (USE_MOCK) return Promise.resolve(mockTrunkEgress(window));
	return get<TrunkWireChart>('/api/v1/admin/trunk/egress', { window });
}

function mockTrunkEgress(window: TrunkWindow): TrunkWireChart {
	const bucketSeconds = window === '6h' ? 60 : window === '24h' ? 300 : window === '7d' ? 1800 : 3600;
	const n = window === '6h' ? 360 : window === '24h' ? 288 : window === '7d' ? 336 : 720;
	const end = Math.floor(Date.now() / 1000 / bucketSeconds) * bucketSeconds;
	const buckets = Array.from({ length: n }, (_, i) => new Date((end - (n - 1 - i) * bucketSeconds) * 1000).toISOString());
	const keys = ['unl1 → aeznl1', 'unl1 → spare1'];
	const per = bucketSeconds / 60;
	const lines = keys.flatMap((key, k) => [
		{ key, group: 'egress_tun', values: buckets.map((_, i) => (k === 0 && i === n - 5 ? 40 : 0)) },
		{ key, group: 'egress_net', values: buckets.map((_, i) => (k === 0 ? (i === n - 5 ? 40 : 0) : null)) },
		{ key, group: 'egress_rtt', values: buckets.map(() => 10 + k * 3 + Math.random()) },
		{ key, group: 'egress_carry', values: buckets.map((_, i) => ((i < n - 5) === (k === 0) ? per : 0)) },
	]);
	return {
		window,
		bucketSeconds,
		buckets,
		lines,
		marks: [{ at: buckets[n - 5], label: 'unl1: выход aeznl1 умер, на spare1 (tunnel silent 5 s)', class: 'egress', why: 'red' }],
	};
}
