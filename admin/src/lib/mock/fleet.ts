import type { FleetProfile, FleetProfiles, FleetWindow } from '$lib/api/fleet';

// Fixture for the fleet screen, shaped after the profiles on the stand: UI
// work only, never a stand-in for the live endpoint (see config/mock.ts).

const BUCKET: Record<FleetWindow, number> = { '24h': 3600, '7d': 21600, '30d': 86400 };
const SPAN: Record<FleetWindow, number> = { '24h': 86400, '7d': 7 * 86400, '30d': 30 * 86400 };

function wave(n: number, base: number, swing: number): number[] {
	return Array.from({ length: n }, (_, i) => Math.max(0, base + swing * Math.sin(i / 3) + swing * 0.3 * Math.cos(i)));
}

export function mockFleetProfiles(window: FleetWindow): FleetProfiles {
	const bucketSeconds = BUCKET[window];
	const end = Math.floor(Date.now() / 1000 / bucketSeconds) * bucketSeconds;
	const n = SPAN[window] / bucketSeconds + 1;
	const buckets = Array.from({ length: n }, (_, i) =>
		new Date((end - (n - 1 - i) * bucketSeconds) * 1000).toISOString(),
	);
	const ago = (days: number) => new Date(Date.now() - days * 86400_000).toISOString();
	const month = new Date(Date.UTC(new Date().getUTCFullYear(), new Date().getUTCMonth(), 1)).toISOString();
	const profiles: FleetProfile[] = [
		{
			label: 'amsterdam-1-001',
			slug: 'd123a171',
			relay: 'ams3-1',
			box: 'ams3',
			proto: 'vless-reality-vision',
			provider: 'vdsina',
			location: 'Amsterdam 1, nl',
			plan: '36',
			monthlyCents: 499,
			cover: 'www.samsung.com',
			fingerprint: 'chrome_155',
			boundAt: ago(32),
			trafficUsedGb: 1240,
			trafficQuotaGb: 32768,
			trafficSince: month,
			availPct: wave(n, 97, 3).map((v) => Math.min(100, v)),
			rttMs: wave(n, 303, 25),
			carriedMbps: wave(n, 3.2, 2.1),
		},
		{
			label: 'eu-west1-001',
			slug: 'd9310bb2',
			relay: 'euw1-1',
			box: 'euw1',
			proto: 'vless-xhttp',
			provider: 'openstack',
			location: 'Amsterdam, NL',
			cover: 'example-cover.site',
			fingerprint: 'chrome_133',
			boundAt: ago(6),
			trafficUsedGb: 312,
			trafficQuotaGb: 10000,
			trafficSince: month,
			availPct: wave(n, 99, 1).map((v) => Math.min(100, v)),
			rttMs: wave(n, 311, 30),
			carriedMbps: wave(n, 1.4, 1.2),
		},
		{
			label: 'рига-003',
			slug: 'f7fc0cff',
			relay: 'eun16-2',
			box: 'eun16',
			proto: 'hysteria2',
			provider: 'openstack',
			location: 'Riga, LV',
			cover: 'xonhmhunv.com',
			boundAt: ago(0.75),
			trafficUsedGb: 4.2,
			trafficQuotaGb: 10000,
			trafficSince: ago(0.75),
			availPct: wave(n, 100, 0),
			rttMs: wave(n, 31, 4),
			carriedMbps: wave(n, 0.6, 0.5),
		},
	];
	return { window, bucketSeconds, buckets, profiles, missing: [] };
}
