import { get } from './client';
import { USE_MOCK } from '$lib/config/mock';
import type { TrunkWindow, TrunkWireChart } from './trunkWires';

// The entries' border points (G5, hub internal/cascadewatch point.go): per
// entry and point the seconds it carried nothing (no member of the trunk up
// through it) and its members up; the brain's changes as marks. Keys are
// "<entry> → <point>", the stand's with " (стенд)".

export async function fetchTrunkPoints(window: TrunkWindow): Promise<TrunkWireChart> {
	if (USE_MOCK) return Promise.resolve(mockTrunkPoints(window));
	return get<TrunkWireChart>('/api/v1/admin/trunk/points', { window });
}

function mockTrunkPoints(window: TrunkWindow): TrunkWireChart {
	const bucketSeconds = window === '6h' ? 600 : window === '24h' ? 3600 : 21600;
	const n = window === '6h' ? 36 : window === '24h' ? 24 : 28;
	const end = Math.floor(Date.now() / 1000 / bucketSeconds) * bucketSeconds;
	const buckets = Array.from({ length: n }, (_, i) => new Date((end - (n - 1 - i) * bucketSeconds) * 1000).toISOString());
	const keys = ['aezamsk1 → unl1', 'aezamsk1 → ams3 (стенд)'];
	const lines = keys.flatMap((key, k) => [
		{ key, group: 'point_silent', values: buckets.map((_, i) => (k === 0 && i === n - 4 ? 150 : 0)) },
		{ key, group: 'point_up', values: buckets.map((_, i) => (k === 0 && i === n - 4 ? 0 : 19)) },
	]);
	return {
		window,
		bucketSeconds,
		buckets,
		lines,
		marks: [{ at: buckets[n - 4], label: 'aezamsk1: точка unl1 молчит, 0 из 19 членов', class: 'point', why: 'red' }],
	};
}
