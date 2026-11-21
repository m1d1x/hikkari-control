import { get } from './client';
import { USE_MOCK } from '$lib/config/mock';
import type { TrunkWindow, TrunkWireChart } from './trunkWires';

// The entries' border points (G5, hub internal/cascadewatch point.go): per
// entry and point the seconds it carried nothing (no member of the trunk up
// through it) and its members up; the brain's changes as marks. Keys are
// "<entry> → <point>", the stand's with " (стенд)".

export interface PointSample {
	at: string;
	up: number;
	members: number;
	silentSeconds: number;
}

export interface PointRoute {
	entry: string;
	exit: string;
	points: { name: string; sample: PointSample | null }[];
}

export interface TrunkPointChart extends TrunkWireChart {
	routes: PointRoute[];
}

export async function fetchTrunkPoints(window: TrunkWindow): Promise<TrunkPointChart> {
	if (USE_MOCK) return Promise.resolve(mockTrunkPoints(window));
	return get<TrunkPointChart>('/api/v1/admin/trunk/points', { window });
}

function mockTrunkPoints(window: TrunkWindow): TrunkPointChart {
	const bucketSeconds = window === '6h' ? 60 : window === '24h' ? 600 : 3600;
	const n = window === '6h' ? 360 : window === '24h' ? 144 : 168;
	const end = Math.floor(Date.now() / 1000 / bucketSeconds) * bucketSeconds;
	const buckets = Array.from({ length: n }, (_, i) =>
		new Date((end - (n - 1 - i) * bucketSeconds) * 1000).toISOString(),
	);
	const outageFrom = n - Math.ceil(n / 8);
	const silence = (i: number) =>
		i < outageFrom ? 0 : Math.max(0, Math.min(bucketSeconds, 150 - (i - outageFrom) * bucketSeconds));
	const keys = ['aezamsk1 → unl1', 'aezamsk1 → hznl1', 'aezamsk1 → hznl1 (стенд)'];
	const lines = keys.flatMap((key, k) => [
		{
			key,
			group: 'point_silent',
			values: buckets.map((_, i) => (k === 0 ? silence(i) : 0)),
		},
		{ key, group: 'point_up', values: buckets.map((_, i) => (k === 0 && silence(i) ? 0 : 19)) },
	]);
	return {
		window,
		bucketSeconds,
		buckets,
		lines: [
			...lines,
			{
				key: 'aezamsk1 → aeznl1',
				group: 'point_call_gap',
				values: buckets.map((_, i) => (silence(i) ? 56 : 34 + (i % 8))),
			},
		],
		marks: [
			{
				at: buckets[outageFrom],
				label: 'aezamsk1: точка unl1 молчит, 0 из 19 соединений',
				class: 'point',
				why: 'red',
			},
			{
				at: new Date(Date.parse(buckets[outageFrom]) + 150_000).toISOString(),
				label: 'aezamsk1: точка unl1 снова везёт, 19 из 19 соединений, молчала 150 с',
				class: 'point',
				why: 'ok',
			},
		],
		routes: [
			{
				entry: 'aezamsk1',
				exit: 'aeznl1',
				points: ['unl1', 'hznl1'].map((name, i) => ({
					name,
					sample: { at: new Date(Date.now() - 60_000).toISOString(), up: 19, members: 19, silentSeconds: 0 },
				})),
			},
		],
	};
}
