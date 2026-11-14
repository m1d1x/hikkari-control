import { USE_MOCK } from '$lib/config/mock';
import { get } from './client';

// The fleet screen (hub internal/admin/fleet_profiles.go): every profile a
// subscriber is handed now, with what it is and how it lived over the window.

export type FleetWindow = '24h' | '7d' | '30d';

export interface FleetProfile {
	/** the name a subscriber reads; the slug when none is set yet */
	label: string;
	slug: string;
	relay: string;
	box: string;
	proto: string;
	/** the hoster's API kind; providerLabel() turns it into the service's name */
	provider: string;
	location: string;
	plan?: string;
	monthlyCents?: number;
	/** the site the profile passes for: an XHTTP box's domain, else the cover it was last probed under */
	cover?: string;
	/** the browser whose ClientHello the link asks the client to imitate */
	fingerprint?: string;
	/** when the profile started being handed out on this relay */
	boundAt: string;
	/** the box's traffic this month, and its plan's allowance */
	trafficUsedGb?: number;
	trafficQuotaGb?: number;
	trafficSince: string;
	/** per prober: share of the call-sized datagrams sent through the relay that never came back */
	loss: { source: string; pct: (number | null)[] }[];
	/** share of the probers' rounds the relay carried nothing in: banned, a silent tunnel, no datagram back */
	failPct: (number | null)[];
	/** share of the TCP segments the relay's box sent that it had to send again; null below 2000 segments */
	retransPct: (number | null)[];
	/** median latency of the RU probers' check through the relay */
	rttMs: (number | null)[];
	/** download speed the RU probers measured through the profile, median per bucket */
	speedMbps: (number | null)[];
	/** the node's usual speed: the median of the same measurements over the past week */
	speedNormMbps?: number;
	/** what the relay passed for subscribers, the busier direction: load, not speed */
	carriedMbps: (number | null)[];
}

/**
 * A cascade (hub internal/admin/fleet_cascades.go): a trunk entry in Russia
 * and its exit, under the name a subscriber reads for the cascade link, with
 * its two legs as history. Loss is per 1000; a bucket nothing was sent in is
 * null.
 */
export interface FleetCascade {
	label: string;
	entry: string;
	entryLocation: string;
	entryProvider: string;
	exit: string;
	exitLocation: string;
	/** client → entry: TCP segments the entry sent its clients again, per 1000 sent */
	clientLossPm: (number | null)[];
	/** client → entry: median of the minutes' p50 round trip of the clients' sockets */
	clientRttMs: (number | null)[];
	/** entry → exit: the trunk's call with one copy, lost per 1000 */
	trunkLossPm: (number | null)[];
	/** entry → exit: median of the call's minutes' p50 round trip */
	trunkRttMs: (number | null)[];
}

export interface FleetProfiles {
	window: FleetWindow;
	bucketSeconds: number;
	buckets: string[];
	profiles: FleetProfile[];
	cascades: FleetCascade[];
	missing: { figure: string; why: string }[];
}

export async function fetchFleetProfiles(window: FleetWindow): Promise<FleetProfiles> {
	if (USE_MOCK) return (await import('$lib/mock/fleet')).mockFleetProfiles(window);
	return get<FleetProfiles>('/api/v1/admin/fleet/profiles', { window });
}
