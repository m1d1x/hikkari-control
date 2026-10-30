import { get, post } from './client';
import * as mock from '$lib/mock/system';
import { USE_MOCK } from '$lib/config/mock';
import type {
	SystemOverview,
	ServiceHealth,
	UptimeResponse,
	ServiceLogs,
	Incident,
	BlueGreenStatus,
	ProbesResponse,
	ActiveOpsAlert,
	SyntheticStep,
} from '$lib/types/system';

function parseLatency(s: string | number | undefined): number {
	if (typeof s === 'number') return s;
	if (!s) return 0;
	if (s.includes('\u00b5s') || s.includes('us')) return parseFloat(s) / 1000;
	if (s.includes('ms')) return parseFloat(s);
	if (s.includes('s')) return parseFloat(s) * 1000;
	return parseFloat(s) || 0;
}

function mapStatus(s: string): ServiceHealth['status'] {
	if (s === 'ok' || s === 'healthy') return 'healthy';
	if (s === 'degraded' || s === 'warn') return 'degraded';
	if (s === 'down' || s === 'error') return 'down';
	return 'unknown';
}

export const getSystemOverview = USE_MOCK
	? mock.getSystemOverview
	: async (): Promise<SystemOverview> => {
			const raw = await get<any>('/api/v1/admin/system/overview');
			const services: ServiceHealth[] = (raw.services || []).map((s: any) => ({
				name: s.name,
				status: mapStatus(s.status),
				latency: Math.round(parseLatency(s.latency) * 100) / 100,
				// No default: a backend that reported no uptime must surface as
				// "no data", not as 99.9% (plan 42 П9.9).
				uptime: s.uptime ?? null,
				lastCheck: s.lastCheck || new Date().toISOString(),
				metrics: s.metrics || {},
			}));

			const allHealthy = services.every((s) => s.status === 'healthy');
			const anyDown = services.some((s) => s.status === 'down');

			return {
				services,
				alerts: raw.alerts || [],
				overallStatus: anyDown ? 'down' : allHealthy ? 'healthy' : 'degraded',
				runtime: raw.runtime || null,
				system: raw.system || null,
				thresholds: raw.thresholds || [],
				prometheusAvailable: raw.prometheusAvailable ?? false,
			};
		};

export const getServiceUptime = USE_MOCK
	? mock.getServiceUptime
	: async (period: string = '24h'): Promise<UptimeResponse> => {
			return get<UptimeResponse>('/api/v1/admin/system/uptime', { period });
		};

export const getServiceLogs = USE_MOCK
	? mock.getServiceLogs
	: async (service: string, limit: number = 100): Promise<ServiceLogs> => {
			return get<ServiceLogs>('/api/v1/admin/system/logs', { service, limit });
		};

export async function getIncidents(period: string = '24h'): Promise<Incident[]> {
	if (USE_MOCK) return mock.getIncidents(period);
	const data = await get<{ incidents: Incident[] }>('/api/v1/admin/system/incidents', { period });
	return data.incidents || [];
}

export async function acknowledgeAlert(id: string): Promise<void> {
	if (USE_MOCK) return;
	await post('/api/v1/admin/system/alerts/' + id + '/ack', {});
}

export async function acknowledgeIncident(id: string): Promise<void> {
	if (USE_MOCK) return;
	await post('/api/v1/admin/system/incidents/' + id + '/ack', {});
}

export const getBlueGreenStatus = USE_MOCK
	? mock.getBlueGreenStatus
	: async (): Promise<BlueGreenStatus> => {
			return get<BlueGreenStatus>('/api/v1/admin/system/blue-green');
		};

// External + internal probe history from admin-server (ClickHouse-backed,
// survives restarts). UI uses this for the per-service ProbeBar tracks.
// Falls through mock mode by returning an empty payload — the component
// degrades gracefully (renders "no data" placeholder instead of bars).
export async function getProbes(period: string = '24h'): Promise<ProbesResponse> {
	if (USE_MOCK) {
		return { period, bucket_seconds: 0, internal: {}, external: {} };
	}
	return get<ProbesResponse>('/api/v1/admin/system/probes', { period });
}

// Currently-firing alerts from alerting.Engine — same signal that's paging
// Telegram. Polled fast (10s) so the dashboard stays in sync with TG. Mock
// mode returns empty so storybook/local dev doesn't fake outages.
export async function getActiveOpsAlerts(): Promise<ActiveOpsAlert[]> {
	if (USE_MOCK) return [];
	const data = await get<{ alerts: ActiveOpsAlert[] }>(
		'/api/v1/admin/system/alerts/active',
	);
	return data.alerts || [];
}

// Synthetic-journey snapshot for the per-step traffic-light board.
// Optional `range` (1h|6h|24h|7d) populates step.series for inline sparklines
// — omitting it returns instant-only data (cheaper, also what the fast 10s
// poll uses). Returns [] on Prometheus errors so the UI degrades to empty
// cards rather than showing a console-error banner the operator can't act on.
export async function getSyntheticJourney(range?: string): Promise<SyntheticStep[]> {
	if (USE_MOCK) return [];
	try {
		const params: Record<string, string> = {};
		if (range) params.range = range;
		const data = await get<{ steps: SyntheticStep[] }>(
			'/api/v1/admin/system/synthetic',
			params,
		);
		return data.steps || [];
	} catch (err) {
		console.warn('[Synthetic] fetch failed:', err);
		return [];
	}
}

export async function getMetricsHistory(
	query: string,
	range: string = '24h',
): Promise<{ ts: number; value: number }[]> {
	if (USE_MOCK) return [];
	const data = await get<{ points: { ts: number; value: number }[] }>(
		'/api/v1/admin/system/metrics/history',
		{ query, range },
	);
	return data.points || [];
}
