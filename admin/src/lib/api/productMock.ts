import type { DowntimePerson, MetricsShare, ProductDowntime, ProductMetrics, TimeSeries } from './product';
import type { Win } from '$lib/product/format';

// Fixture for `VITE_USE_MOCK=true`: product overview with believable figures.
// Deterministic; a handful of testers, not a user base.

const SHAPE: Record<Win, { n: number; step: number }> = {
	'24h': { n: 24, step: 3600 },
	'7d': { n: 28, step: 6 * 3600 },
	'30d': { n: 30, step: 86400 },
} as Record<Win, { n: number; step: number }>;

const series = (label: string, n: number, dayStep: number, base: number, amp: number): TimeSeries => {
	const end = Math.floor(Date.now() / 86400000) * 86400000;
	return {
		label,
		data: Array.from({ length: n }, (_, i) => ({
			timestamp: new Date(end - (n - 1 - i) * dayStep * 86400000).toISOString().slice(0, 10),
			value: Math.max(0, Math.round(base + amp * Math.sin(i / 2.2) + (i % 4))),
		})),
	};
};

const share = (rows: [string, number][]): MetricsShare[] => {
	const total = rows.reduce((a, [, n]) => a + n, 0);
	return rows.map(([key, installs]) => ({ key, installs, pct: +((installs / total) * 100).toFixed(1) }));
};

export function mockProductMetrics(win: Win): ProductMetrics {
	const { n, step } = SHAPE[win] ?? SHAPE['7d'];
	const end = Math.floor(Date.now() / 1000 / step) * step;
	const buckets = Array.from({ length: n }, (_, i) => (end - (n - 1 - i) * step) * 1000);
	return {
		window: win,
		bucketSeconds: step,
		buckets,
		online: {
			now: 3,
			series: buckets.map((_, i) =>
				i < 3 ? null : Math.max(0, Math.round(2 + 2 * Math.sin(i / 2.5) + (i % 3) * 0.6)),
			),
		},
		people: {
			active: 9,
			fresh: 3,
			returning: 6,
			total: 14,
			dau: 5,
			wau: 9,
			mau: 13,
			stickiness: 38.5,
			dauTimeSeries: series('DAU', 30, 1, 5, 1.5),
			wauTimeSeries: series('WAU', 12, 7, 9, 1.5),
			mauTimeSeries: series('MAU', 6, 30, 12, 1.5),
			stickinessTimeSeries: series('Липкость', 30, 1, 38, 6),
			series: {
				active: buckets.map((_, i) => 4 + Math.round(2 * Math.sin(i / 3)) + (i % 3)),
				fresh: buckets.map((_, i) => (i % 9 === 2 ? 1 : 0)),
			},
		},
		connect: { attempts: 86, okPct: 94.2, secsP50: 3.4, tunnelMinutesP50: 142 },
		activation: { installs: 4, connected: 3, pct: 75, secsP50: 41 },
		downloads: {
			clicks: 21,
			clicksSeries: buckets.map((_, i) => (i % 5 === 1 ? 2 : i % 3 === 0 ? 1 : 0)),
			installs: 4,
			convPct: 19,
			platforms: share([
				['windows', 14],
				['macos', 5],
				['android', 2],
			]),
		},
		platforms: share([
			['windows', 8],
			['macos', 4],
			['android', 1],
			['linux', 1],
		]),
		versions: share([
			['1.0.58', 7],
			['1.0.57', 4],
			['1.0.55', 2],
			['1.0.52', 1],
		]),
	};
}

/** Three people over the window, one with a bad evening; demo data for VITE_USE_MOCK. */
export function mockProductDowntime(win: Win): ProductDowntime {
	const step = win === '24h' ? 3600 : win === '7d' ? 6 * 3600 : 86400;
	const n = win === '24h' ? 24 : win === '7d' ? 28 : 30;
	const end = Math.floor(Date.now() / 1000 / step) * step;
	const buckets = Array.from({ length: n }, (_, i) => new Date((end - (n - 1 - i) * step) * 1000).toISOString());
	const person = (name: string, f: (i: number) => number | null): DowntimePerson => {
		const lost = buckets.map((_, i) => f(i));
		return {
			install: `t-${name}`,
			name,
			lost,
			on: lost.map((v) => (v === null ? null : step / 60)),
			net: lost.map((v) => (v === null ? null : 0)),
			lostTotal: lost.reduce<number>((a, v) => a + (v ?? 0), 0),
		};
	};
	const people = [
		person('tester-002', (i) => (i === n - 5 ? 23 : i === n - 4 ? 9 : i % 7 === 3 ? 1 : 0)),
		person('tester-001', (i) => (i % 9 === 4 ? 2 : 0)),
		person('tester-003', (i) => (i < n / 3 ? null : 0)),
	];
	const average = buckets.map((_, i) => {
		const on = people.filter((p) => p.lost[i] !== null);
		return on.length ? on.reduce((a, p) => a + (p.lost[i] ?? 0), 0) / on.length : null;
	});
	const averageTotal = people.reduce((a, p) => a + p.lostTotal, 0) / people.length;
	return { window: win, bucketSeconds: step, buckets, average, averageTotal, people };
}
