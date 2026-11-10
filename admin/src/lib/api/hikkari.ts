import { get } from './client';
import { USE_MOCK } from '$lib/config/mock';

// Hikkari v1.04 observability (hub internal/hikkariobs): what the protocol's
// own sessions saw, at the relay and in the client's core, over time.

export type HikkariWindow = '6h' | '24h' | '7d';

export interface HikkariLine {
	key: string;
	/** which chart it belongs to */
	group: string;
	/** one value per bucket; null where nothing was counted */
	values: (number | null)[];
}

export interface HikkariChart {
	window: HikkariWindow;
	bucketSeconds: number;
	buckets: string[];
	lines: HikkariLine[];
}

export async function fetchHikkariChart(window: HikkariWindow): Promise<HikkariChart> {
	if (USE_MOCK) return Promise.resolve(mockHikkariChart(window));
	return get<HikkariChart>('/api/v1/admin/hikkari/chart', { window });
}

const MOCK_GROUPS: Record<string, string[]> = {
	dup_saved: ['saved_down'],
	dup_first: ['first_stream_down', 'first_stream_up'],
	dup_lead: ['lead_stream_down', 'lead_dgram_down'],
	calls: ['calls', 'calls_short', 'calls_one_sided'],
	trunk_ends: ['trunk_eof', 'trunk_reset', 'trunk_idle', 'trunk_error'],
	trunk_tcp: ['trunk_retrans'],
	trunk_rtt: ['trunk_rtt'],
	naive_tcp: ['naive_retrans'],
	naive_rtt: ['naive_rtt'],
	quic_loss: ['dgram_loss_up', 'voice_loss_up', 'dgram_loss_down'],
	tunnel_ends: ['tunnels_eof', 'tunnels_reset', 'tunnels_idle', 'tunnels_revoked', 'tunnels_stop', 'tunnels_error'],
};

/** A plausible day for UI work without a backend (VITE_USE_MOCK). */
function mockHikkariChart(window: HikkariWindow): HikkariChart {
	const bucketSeconds = window === '6h' ? 600 : window === '24h' ? 3600 : 21600;
	const n = window === '6h' ? 36 : window === '24h' ? 24 : 28;
	const end = Math.floor(Date.now() / 1000 / bucketSeconds) * bucketSeconds;
	const buckets = Array.from({ length: n }, (_, i) =>
		new Date((end - (n - 1 - i) * bucketSeconds) * 1000).toISOString(),
	);
	let seed = 7;
	const rnd = () => (seed = (seed * 16807) % 2147483647) / 2147483647;
	const lines: HikkariLine[] = [];
	for (const [group, keys] of Object.entries(MOCK_GROUPS)) {
		keys.forEach((key, k) => {
			const scale = 1 + k * 2 + rnd() * 10;
			lines.push({
				key,
				group,
				values: buckets.map((_, i) =>
					i === 5 ? null : Math.max(0, scale * (1 + Math.sin(i / 3 + k)) + rnd() * scale),
				),
			});
		});
	}
	return { window, bucketSeconds, buckets, lines };
}
