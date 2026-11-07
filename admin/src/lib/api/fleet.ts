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
	/** share of the RU probers' verdicts that got through; a bucket nobody judged is null */
	availPct: (number | null)[];
	/** median latency of the RU probers' check through the relay */
	rttMs: (number | null)[];
	/** what the relay passed for subscribers, the busier direction, counted on the relay */
	carriedMbps: (number | null)[];
}

export interface FleetProfiles {
	window: FleetWindow;
	bucketSeconds: number;
	buckets: string[];
	profiles: FleetProfile[];
	missing: { figure: string; why: string }[];
}

export async function fetchFleetProfiles(window: FleetWindow): Promise<FleetProfiles> {
	if (USE_MOCK) return (await import('$lib/mock/fleet')).mockFleetProfiles(window);
	return get<FleetProfiles>('/api/v1/admin/fleet/profiles', { window });
}
