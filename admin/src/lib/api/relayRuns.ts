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

/** A line of the knob controller's journal (hub internal/stand controller.go). */
export interface RelayKnobEvent {
	at: string;
	node: string;
	profile: string;
	knob: string;
	kind: 'switch' | 'all_red' | 'address_cut' | 'network_red';
	/** The subscribers' network the line is about; absent for the node's own value. */
	asn?: number;
	from?: string;
	to?: string;
	reason?: string;
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
	events: RelayKnobEvent[] | null;
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
