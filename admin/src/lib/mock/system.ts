import type {
	SystemOverview,
	ServiceHealth,
	DataQualityAlert,
	UptimeResponse,
	UptimePoint,
	ServiceLogs,
	RuntimePoint,
	SystemMetricsPoint,
	S3StoragePoint,
	BlueGreenStatus,
} from '$lib/types/system';
import { generateUUID, minutesAgo, hoursAgo, randomInt, randomFloat } from './generators';
import { withDelay } from './delay';

export function getSystemOverview(): Promise<SystemOverview> {
	return withDelay({
		overallStatus: 'healthy',
		services: getServices(),
		alerts: getAlerts(),
		runtime: {
			goroutines: 142,
			heapAlloc: '48.2 MB',
			heapSys: '128.0 MB',
			gcRuns: 1204,
			goVersion: 'go1.25.0',
			serverUptime: 291957,
			serverUptimeFormatted: '3d 9h 5m',
		},
		system: {
			cpuPercent: 23.5,
			memUsed: 1_825_361_920,
			memTotal: 4_294_967_296,
			memPercent: 42.5,
			diskUsed: 18_253_611_008,
			diskTotal: 53_687_091_200,
			diskPercent: 34.0,
		},
	});
}

export function getServiceUptime(period: string = '24h'): Promise<UptimeResponse> {
	const hours = period === '1h' ? 1 : period === '6h' ? 6 : 24;
	const points = Math.floor((hours * 60) / 5); // 5-min intervals

	const services: Record<string, { uptime: number; checks: UptimePoint[] }> = {};
	const latencyHistory: Record<string, number[]> = {};

	for (const name of ['postgresql', 'redis', 'clickhouse', 'sfu', 's3']) {
		const checks: UptimePoint[] = [];
		const latencies: number[] = [];
		let okCount = 0;
		for (let i = points; i >= 0; i--) {
			const ts = new Date(Date.now() - i * 300000).toISOString();
			const isOk = Math.random() > 0.003;
			const lat = isOk ? randomFloat(0.0005, 0.015) : 0;
			checks.push({ ts, status: isOk ? 'ok' : 'error', latency: lat });
			latencies.push(lat);
			if (isOk) okCount++;
		}
		services[name] = {
			uptime: Math.round((okCount / checks.length) * 10000) / 100,
			checks,
		};
		latencyHistory[name] = latencies;
	}

	const runtimePoints: RuntimePoint[] = [];
	for (let i = points; i >= 0; i--) {
		runtimePoints.push({
			ts: new Date(Date.now() - i * 300000).toISOString(),
			goroutines: randomInt(120, 180),
			heapAlloc: randomInt(40000000, 60000000),
			heapSys: randomInt(120000000, 140000000),
			gcRuns: 1200 + Math.floor((points - i) / 10),
		});
	}

	const sysMetrics: SystemMetricsPoint[] = [];
	for (let i = points; i >= 0; i--) {
		sysMetrics.push({
			ts: new Date(Date.now() - i * 300000).toISOString(),
			cpuPercent: randomFloat(15, 45),
			memUsed: randomInt(1_600_000_000, 2_000_000_000),
			memTotal: 4_294_967_296,
			memPercent: randomFloat(38, 48),
			diskUsed: randomInt(18_000_000_000, 18_500_000_000),
			diskTotal: 53_687_091_200,
			diskPercent: randomFloat(33, 35),
		});
	}

	// Recent raw metrics (30s intervals, last hour = 120 points)
	const recentMetrics: SystemMetricsPoint[] = [];
	for (let i = 120; i >= 0; i--) {
		recentMetrics.push({
			ts: new Date(Date.now() - i * 30000).toISOString(),
			cpuPercent: randomFloat(15, 45),
			memUsed: randomInt(1_600_000_000, 2_000_000_000),
			memTotal: 4_294_967_296,
			memPercent: randomFloat(38, 48),
			diskUsed: randomInt(18_000_000_000, 18_500_000_000),
			diskTotal: 53_687_091_200,
			diskPercent: randomFloat(33, 35),
		});
	}

	// S3 storage growth over time (simulates gradual growth with small random bumps)
	const s3History: S3StoragePoint[] = [];
	const baseBytes = 2_500_000_000; // ~2.5 GB starting point
	const baseObjects = 13_000;
	for (let i = points; i >= 0; i--) {
		const growth = (points - i) * 1_200_000; // ~1.2 MB per interval
		s3History.push({
			ts: new Date(Date.now() - i * 300000).toISOString(),
			usedBytes: baseBytes + growth + randomInt(0, 500_000),
			objects: baseObjects + Math.floor((points - i) * 4) + randomInt(0, 3),
		});
	}

	return withDelay({ services, runtime: runtimePoints, latencyHistory, systemMetrics: sysMetrics, recentSystemMetrics: recentMetrics, s3History });
}

export function getIncidents(_period: string = '24h'): Promise<any[]> {
	const now = Date.now();
	return withDelay([
		{
			id: 'inc-1',
			service: 'redis',
			startedAt: new Date(now - 7200000).toISOString(),
			resolvedAt: new Date(now - 6900000).toISOString(),
			duration: '5m',
			type: 'degraded',
			checks: 1,
			acknowledged: false,
		},
		{
			id: 'inc-2',
			service: 'clickhouse',
			startedAt: new Date(now - 43200000).toISOString(),
			resolvedAt: new Date(now - 42600000).toISOString(),
			duration: '10m',
			type: 'downtime',
			checks: 2,
			acknowledged: false,
		},
	]);
}

export function getServiceLogs(
	service: string,
	_limit: number = 100,
): Promise<ServiceLogs> {
	const entries = generateMockLogs(service);
	return withDelay({ service, entries });
}

function generateMockLogs(service: string) {
	const now = Date.now();
	const logs: { ts: string; level: 'info' | 'warn' | 'error'; message: string }[] = [];

	switch (service) {
		case 'redis':
			logs.push(
				{ ts: new Date(now - 60000).toISOString(), level: 'info', message: 'Redis 7.2.6, uptime: 432000s' },
				{ ts: new Date(now - 120000).toISOString(), level: 'warn', message: 'Slow command (85ms): KEYS hikkari:discover:*' },
				{ ts: new Date(now - 300000).toISOString(), level: 'info', message: 'Background save completed successfully' },
				{ ts: new Date(now - 600000).toISOString(), level: 'info', message: 'RDB: 1024 changes in 60 seconds. Saving...' },
				{ ts: new Date(now - 900000).toISOString(), level: 'warn', message: 'Slow command (120ms): SMEMBERS hikkari:server:large-set' },
			);
			break;
		case 'clickhouse':
			logs.push(
				{ ts: new Date(now - 30000).toISOString(), level: 'info', message: '[QueryFinish] 12ms, 1240 rows \u2014 SELECT count() FROM events WHERE...' },
				{ ts: new Date(now - 90000).toISOString(), level: 'info', message: '[QueryFinish] 45ms, 284500 rows \u2014 SELECT event_type, count()...' },
				{ ts: new Date(now - 180000).toISOString(), level: 'warn', message: '[QueryFinish] 1200ms, 12000000 rows \u2014 SELECT * FROM events WHERE...' },
				{ ts: new Date(now - 360000).toISOString(), level: 'info', message: '[MergeCompleted] Merged 4 parts into 1 (12.4 MB \u2192 8.1 MB)' },
				{ ts: new Date(now - 720000).toISOString(), level: 'error', message: '[QueryError] Table hikkari_analytics.missing_table does not exist' },
			);
			break;
		case 'postgresql':
			logs.push(
				{ ts: new Date(now - 15000).toISOString(), level: 'info', message: '[pid:142] active (0s) \u2014 SELECT * FROM accounts WHERE id = $1' },
				{ ts: new Date(now - 45000).toISOString(), level: 'info', message: '[pid:143] idle (2s) \u2014 COMMIT' },
				{ ts: new Date(now - 120000).toISOString(), level: 'warn', message: '[pid:144] active (35s) \u2014 SELECT * FROM servers JOIN members ON...' },
				{ ts: new Date(now - 300000).toISOString(), level: 'info', message: '[pid:145] idle in transaction (1s) \u2014 UPDATE accounts SET...' },
			);
			break;
		case 'sfu':
			logs.push(
				{ ts: new Date(now - 5000).toISOString(), level: 'info', message: 'SFU online: 48 users connected' },
				{ ts: new Date(now - 60000).toISOString(), level: 'info', message: 'Room created: server-abc-voice-1 (3 participants)' },
				{ ts: new Date(now - 180000).toISOString(), level: 'info', message: 'User left room: server-xyz-voice-2' },
			);
			break;
	}
	return logs;
}

function getServices(): ServiceHealth[] {
	return [
		{
			name: 'PostgreSQL',
			status: 'healthy',
			latency: 1.2,
			uptime: 99.97,
			lastCheck: minutesAgo(0),
			metrics: {
				connections: '34',
				dbSize: '4.2 GB',
				cacheHitRate: '98.5%',
				txTotal: '1284032',
			},
		},
		{
			name: 'Redis',
			status: 'healthy',
			latency: 0.4,
			uptime: 99.99,
			lastCheck: minutesAgo(0),
			metrics: {
				memory: '2.1 GB',
				keys: '124000',
				opsPerSec: '15200',
				hitRate: '94.2%',
				connectedClients: '48',
			},
		},
		{
			name: 'ClickHouse',
			status: 'healthy',
			latency: 12,
			uptime: 99.95,
			lastCheck: minutesAgo(0),
			metrics: {
				totalEvents: '284500000',
				diskUsage: '12.4 GB',
				activeParts: '24',
				eventsLastHour: '44200',
			},
		},
		{
			name: 'SFU',
			status: 'healthy',
			latency: 0.05,
			uptime: 100,
			lastCheck: minutesAgo(0),
			metrics: {
				inVoiceUsers: '48',
				rooms: '12',
			},
		},
		{
			name: 'S3',
			status: 'healthy',
			latency: 3.2,
			uptime: 99.99,
			lastCheck: minutesAgo(0),
			metrics: {
				bucket: 'hikkari-uploads',
				usedStorage: '2.8 GB',
				objects: '14200',
			},
		},
	];
}

function getAlerts(): DataQualityAlert[] {
	return [
		{
			id: generateUUID(),
			severity: 'critical',
			message: 'Event count dropped 35% in last hour compared to previous',
			source: 'ClickHouse',
			timestamp: minutesAgo(45),
			acknowledged: false,
		},
		{
			id: generateUUID(),
			severity: 'warning',
			message: 'Redis slow log: 3 commands exceeded 100ms threshold',
			source: 'Redis',
			timestamp: hoursAgo(2),
			acknowledged: false,
		},
		{
			id: generateUUID(),
			severity: 'info',
			message: 'ClickHouse disk usage at 62% \u2014 approaching 80% threshold',
			source: 'ClickHouse',
			timestamp: hoursAgo(6),
			acknowledged: true,
		},
		{
			id: generateUUID(),
			severity: 'info',
			message: 'Thompson Sampling: 3 servers with beta > 800 detected',
			source: 'Recommendation Service',
			timestamp: hoursAgo(12),
			acknowledged: false,
		},
	];
}

export function getBlueGreenStatus(): Promise<BlueGreenStatus> {
	const now = Date.now();
	return withDelay({
		generatedAt: new Date(now).toISOString(),
		nodes: [
			{
				name: 'prod-ru',
				addr: '5.42.126.80:50443',
				ok: true,
				color: 'green',
				nodeId: 'prod-ru',
				releaseSha: 'ff4df9f',
				imageTag: 'ghcr.io/m1d1x/hikkari:2026-04-30-ff4df9f',
				uptimeSeconds: 4_280,
				lastSeen: new Date(now).toISOString(),
			},
		],
		history: [
			{
				node: 'prod-ru',
				from: 'blue',
				to: 'green',
				at: new Date(now - 4_280_000).toISOString(),
				fromSha: 'fe06c54',
				toSha: 'ff4df9f',
				fromImage: 'ghcr.io/m1d1x/hikkari:2026-04-29-fe06c54',
				toImage: 'ghcr.io/m1d1x/hikkari:2026-04-30-ff4df9f',
			},
			{
				node: 'prod-ru',
				from: 'green',
				to: 'blue',
				at: new Date(now - 86_400_000 * 2).toISOString(),
				fromSha: '1be7f77',
				toSha: 'fe06c54',
				fromImage: 'ghcr.io/m1d1x/hikkari:2026-04-27-1be7f77',
				toImage: 'ghcr.io/m1d1x/hikkari:2026-04-29-fe06c54',
			},
		],
	});
}
