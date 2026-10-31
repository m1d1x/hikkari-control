import { get } from './client';
import type { Win } from '$lib/product/format';

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
	return get('/api/v1/admin/product/metrics', { window: win });
}

export function fetchProductErrors(win: Win): Promise<ProductErrors> {
	return get('/api/v1/admin/product/errors', { window: win });
}
