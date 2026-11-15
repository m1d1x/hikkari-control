import { get } from './client';
import { USE_MOCK } from '$lib/config/mock';
import type { TrunkLine, TrunkWireChart } from './trunkWires';

// What a listener heard of the trunk's calls (hub internal/cascadewatch
// hearing.go; hikkari-client plans/2026-11-15-live-udp-two-fates.md, раздел
// 4): up a listener at the exit, down one at the entry, a frame a hole when it
// was not there at its moment of play with a jitter buffer of 40, 60 or
// 100 ms. A bucket is a minute up to a day.
//
// Groups: <holes|runs2_|bad_s><buffer>_<up|down>. holes is a share of the
// direction's frames in per cent, runs2 the runs of 2 holes or more, bad_s the
// bad seconds. Targets are the call layer's (trunk-<wire>, pipe-<wire>,
// copies*) and the sets' (trunk-voice for the entry, pipe-voice for the stand
// over hk-pipe).
//
// view trunk: holes, runs and bad seconds at 60 ms, every call and set (the
// /protocols/trunk page). view voice: holes at 40, 60 and 100 ms of the voice
// set only (the fleet's «Качество голоса»).

export type HearingWindow = '6h' | '24h' | '7d' | '30d';
export type HearingView = 'trunk' | 'voice';

export async function fetchTrunkHearing(window: HearingWindow, view: HearingView, entry = ''): Promise<TrunkWireChart> {
	if (USE_MOCK) return Promise.resolve(mockTrunkHearing(window, view));
	const q: Record<string, string> = { window, view };
	if (entry) q.entry = entry;
	return get<TrunkWireChart>('/api/v1/admin/trunk/hearing', q);
}

/** A plausible window for UI work without a backend (VITE_USE_MOCK). */
function mockTrunkHearing(window: HearingWindow, view: HearingView): TrunkWireChart {
	const bucketSeconds = window === '7d' ? 600 : window === '30d' ? 3600 : 60;
	const n = window === '6h' ? 360 : window === '24h' ? 1440 : window === '7d' ? 1008 : 720;
	const end = Math.floor(Date.now() / 1000 / bucketSeconds) * bucketSeconds;
	const buckets = Array.from({ length: n }, (_, i) =>
		new Date((end - (n - 1 - i) * bucketSeconds) * 1000).toISOString(),
	);
	let seed = 7;
	const rnd = () => (seed = (seed * 16807) % 2147483647) / 2147483647;
	const lines: TrunkLine[] = [];
	const keys = view === 'voice' ? ['trunk-voice'] : ['trunk-voice', 'trunk-udp', 'trunk-pool', 'pipe-voice'];
	const worse = (k: string) => (k.endsWith('voice') ? 0.05 : k.endsWith('udp') ? 1 : 0.4);
	for (const dir of ['up', 'down']) {
		const groups =
			view === 'voice'
				? [`holes40_${dir}`, `holes60_${dir}`, `holes100_${dir}`]
				: [`holes60_${dir}`, `runs2_60_${dir}`, `bad_s60_${dir}`];
		for (const group of groups) {
			const scale = group.startsWith('holes40') ? 3 : group.startsWith('holes100') ? 0.4 : 1;
			for (const key of keys) {
				lines.push({
					key,
					group,
					values: buckets.map((_, i) =>
						i % 97 === 3 ? null : group.startsWith('holes') ? rnd() * worse(key) * scale : Math.round(rnd() * worse(key) * 3),
					),
				});
			}
		}
	}
	return { window: window as TrunkWireChart['window'], bucketSeconds, buckets, lines, marks: [] };
}
