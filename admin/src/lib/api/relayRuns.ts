import { get } from './client';

// The relay dataset (hub internal/relayruns): rounds of the matrix stand and of
// testers' [diag:load], drawn by one knob of the config over time.

export type RelayRunWindow = '24h' | '7d' | '30d';

export interface RelayRunSeries {
	key: string;
	value: string;
	vantage: string;
	source: 'stand' | 'tester';
	burstPct: (number | null)[];
	dlOkPct: (number | null)[];
	afterPct: (number | null)[];
	rounds: number[];
	totalRounds: number;
	totalBurst: number;
	totalAsked: number;
}

export interface RelayRunChart {
	window: RelayRunWindow;
	bucketSeconds: number;
	buckets: string[];
	knob: string;
	knobs: string[];
	profile: string;
	profiles: string[];
	series: RelayRunSeries[];
}

export async function fetchRelayRunChart(
	window: RelayRunWindow,
	knob: string,
	profile: string,
): Promise<RelayRunChart> {
	const q: Record<string, string> = { window, knob };
	if (profile) q.profile = profile;
	return get<RelayRunChart>('/api/v1/admin/relay-runs/chart', q);
}
