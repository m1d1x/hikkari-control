import { get } from './client';
import type { Win } from '$lib/product/format';
import { USE_MOCK } from '$lib/config/mock';
import { mockProductDowntime, mockProductMetrics } from './productMock';

// The product section's screens, counted by the hub from the clients'
// telemetry (internal/admin/product_metrics.go, product_errors.go). A person
// is an install: a random per-install pseudonym, never an account. A figure
// with no rows behind it is null, not zero.

export interface TsPoint<T = number> {
	timestamp: string;
	value: T;
}

export interface TimeSeries<T = number> {
	label: string;
	data: TsPoint<T>[];
}

export interface MetricsShare {
	key: string;
	installs: number;
	pct: number;
}

export interface ProductMetrics {
	window: Win;
	bucketSeconds: number;
	/** bucket starts, unix ms, oldest first; the last one is still filling */
	buckets: number[];
	/** installs on the line: the latest count and the busiest minute of each bucket, null where nothing was sampled */
	online: { now: number | null; series: (number | null)[] };
	people: {
		active: number;
		fresh: number;
		returning: number;
		/** every install ever, from the daily rollup */
		total: number;
		dau: number;
		wau: number;
		mau: number;
		stickiness: number | null;
		dauTimeSeries?: TimeSeries<number>;
		wauTimeSeries?: TimeSeries<number>;
		mauTimeSeries?: TimeSeries<number>;
		stickinessTimeSeries?: TimeSeries<number>;
		series: { active: number[]; fresh: number[] };
	};
	connect: {
		attempts: number;
		okPct: number | null;
		/** median time a successful connect took */
		secsP50: number | null;
		/** median minutes in the tunnel per person-day */
		tunnelMinutesP50: number | null;
	};
	/** of the installs born in the window, how many got a working tunnel */
	activation: {
		installs: number;
		connected: number;
		pct: number | null;
		secsP50: number | null;
	};
	/** landing click → first launch; clicks are null until the landing reports them */
	downloads: {
		clicks: number | null;
		clicksSeries: number[] | null;
		installs: number;
		convPct: number | null;
		platforms: MetricsShare[];
	};
	platforms: MetricsShare[];
	versions: MetricsShare[];
}

export interface ErrorGroup {
	id: string;
	/** from the client's fixed dictionary: engine.start, tunnel.dns, … */
	code: string;
	component: string;
	/** the latest scrubbed message of the group */
	message: string;
	occurrences: number;
	installs: number;
	oses: string[];
	/** newest first */
	versions: string[];
	firstSeen: number;
	lastSeen: number;
}

export interface ProductErrors {
	window: Win;
	occurrences: number;
	installs: number;
	groups: ErrorGroup[];
	truncated: boolean;
}

export function fetchProductMetrics(win: Win): Promise<ProductMetrics> {
	if (USE_MOCK) return Promise.resolve(mockProductMetrics(win));
	return get('/api/v1/admin/product/metrics', { window: win });
}

export function fetchProductErrors(win: Win): Promise<ProductErrors> {
	return get('/api/v1/admin/product/errors', { window: win });
}

/**
 * The product's main metric per person (hub internal/downtime): minutes the
 * tunnel was on and carried nothing. Every array has a value per bucket, null
 * where the person's tunnel was not on at all.
 */
export interface DowntimePerson {
	install: string;
	name: string;
	/** minutes on with nothing going: asked and no answer, or the core down */
	lost: (number | null)[];
	/** minutes the tunnel was on, any state */
	on: (number | null)[];
	/** minutes nothing went and the person's own network was down too; not lost */
	net: (number | null)[];
	lostTotal: number;
}

export interface ProductDowntime {
	window: Win;
	bucketSeconds: number;
	/** bucket starts, ISO, oldest first; the last one is still filling */
	buckets: string[];
	/** lost minutes over the people whose tunnel was on in the bucket */
	average: (number | null)[];
	/** the window's lost minutes over the people whose tunnel was on in it */
	averageTotal: number | null;
	/** the most lost first */
	people: DowntimePerson[];
}

export function fetchProductDowntime(win: Win): Promise<ProductDowntime> {
	if (USE_MOCK) return Promise.resolve(mockProductDowntime(win));
	return get('/api/v1/admin/product/downtime', { window: win });
}
