export interface ServiceHealth {
	name: string;
	status: 'healthy' | 'degraded' | 'down' | 'unknown';
	latency: number; // ms
	// Percentage, or null when the backend reported none. Never defaulted to a
	// plausible figure: a made-up 99.9% inside the live path is worse than a
	// gap, because nothing downstream can tell it from a measurement.
	uptime: number | null;
	lastCheck: string;
	metrics: Record<string, string | number>;
}

export interface DataQualityAlert {
	id: string;
	severity: 'info' | 'warning' | 'critical';
	message: string;
	source: string;
	timestamp: string;
	acknowledged: boolean;
}

export interface RuntimeInfo {
	goroutines: number;
	heapAlloc: string;
	heapSys: string;
	gcRuns: number;
	goVersion: string;
	serverUptime?: number;
	serverUptimeFormatted?: string;
}

export interface ThresholdResult {
	metric: string;
	service: string;
	current: number;
	max: number;
	percent: number;
	level: 'ok' | 'warning' | 'critical';
}

export interface MetricsHistoryPoint {
	ts: number;
	value: number;
}

export interface SystemOverview {
	services: ServiceHealth[];
	alerts: DataQualityAlert[];
	overallStatus: 'healthy' | 'degraded' | 'down';
	runtime?: RuntimeInfo | null;
	system?: SystemMetrics | null;
	thresholds?: ThresholdResult[];
	prometheusAvailable?: boolean;
}

// ── VPS system metrics ─────────────────────────────────────

export interface SystemMetrics {
	cpuPercent: number;
	memUsed: number;
	memTotal: number;
	memPercent: number;
	diskUsed: number;
	diskTotal: number;
	diskPercent: number;
}

export interface SystemMetricsPoint {
	ts: string;
	cpuPercent: number;
	memUsed: number;
	memTotal: number;
	memPercent: number;
	diskUsed: number;
	diskTotal: number;
	diskPercent: number;
}

// ── Uptime history ─────────────────────────────────────────

export interface UptimePoint {
	ts: string;
	status: 'ok' | 'error' | 'degraded';
	latency: number; // seconds
}

export interface ServiceUptimeData {
	uptime: number;
	checks: UptimePoint[];
}

export interface RuntimePoint {
	ts: string;
	goroutines: number;
	heapAlloc: number;
	heapSys: number;
	gcRuns: number;
}

export interface S3StoragePoint {
	ts: string;
	usedBytes: number;
	objects: number;
}

export interface UptimeResponse {
	services: Record<string, ServiceUptimeData>;
	runtime: RuntimePoint[];
	latencyHistory?: Record<string, number[]>;
	systemMetrics?: SystemMetricsPoint[];
	recentSystemMetrics?: SystemMetricsPoint[];
	s3History?: S3StoragePoint[];
}

// ── Incidents ──────────────────────────────────────────────

export interface Incident {
	id: string;
	service: string;
	startedAt: string;
	resolvedAt?: string;
	duration: string;
	type: 'downtime' | 'degraded';
	checks: number;
	acknowledged: boolean;
}

// ── Service logs ───────────────────────────────────────────

export interface LogEntry {
	ts: string;
	level: 'info' | 'warn' | 'error' | 'debug';
	message: string;
}

export interface ServiceLogs {
	service: string;
	entries: LogEntry[];
}

// ── Blue/green deploy ──────────────────────────────────────

export interface BlueGreenNode {
	name: string;
	addr: string;
	ok: boolean;
	color?: string; // 'blue' | 'green' | ''
	nodeId?: string;
	releaseSha?: string;
	imageTag?: string;
	uptimeSeconds?: number;
	lastSeen?: string;
	error?: string;
}

export interface BlueGreenSwitchEvent {
	node: string;
	from: string;
	to: string;
	at: string;
	fromSha?: string;
	toSha?: string;
	fromImage?: string;
	toImage?: string;
}

export interface BlueGreenStatus {
	generatedAt: string;
	nodes: BlueGreenNode[];
	history: BlueGreenSwitchEvent[];
}

// ── Probe history (admin-server health_probes ClickHouse table) ─

// One bucket from /api/v1/admin/system/probes. When the request period is
// short (≤1h) buckets are raw 10s samples and `n === 1`. For longer periods
// the backend aggregates into time buckets — `status` is worst-of-bucket
// (red wins), `latencyMs` is the average, `errorCount` shows flap density.
export interface ProbeBucket {
	ts: string;
	status: 'ok' | 'error' | 'degraded' | 'timeout' | 'unknown';
	latency_ms: number;
	n: number;
	error_count: number;
}

export interface ProbesResponse {
	period: string;
	bucket_seconds: number; // 0 = raw samples, no aggregation
	internal: Record<string, ProbeBucket[]>;
	external: Record<string, ProbeBucket[]>;
}

// ── Synthetic user journey (admin-server runner) ──────────
//
// One row per step of the loop the admin-server runs every 30s against
// api.nekkochat.com. up=true means the most recent attempt succeeded;
// hasData=false means Prometheus hasn't seen a sample yet (warming up
// after admin-server boot, runner disabled, or step not implemented).
export interface SyntheticStep {
	step: string;
	up: boolean;
	latencyMs: number;
	hasData: boolean;
	// Optional sparkline values of nekko_synthetic_step_up over the requested
	// ?range. Flat float64 array (0/1) in chronological order — empty/undefined
	// when range was not requested or Prometheus returned nothing for that step.
	series?: number[];
}

// ── Active ops alerts (alerting.Engine snapshot) ───────────
//
// Mirrors what's currently paging Telegram. Source/Kind tells the UI which
// channel fired:
//   - source=prom, kind=service_down   → nekko_service_up==0 (HealthTracker)
//   - source=prom, kind=target_down    → up{job=~"nekko-.*"}==0 (built-in scrape)
//   - source=prom, kind=external_down  → nekko_external_probe_up==0 (edge)
//   - source=prom, kind=429_spike      → traffic anomaly
//   - source=thresholds                → in-process AlertManager (CPU/RAM/etc)
//
// Labels carry the dimension that fired (service, job/region, target, ...)
// — used to highlight the matching ServiceRow card.
export interface ActiveOpsAlert {
	source: string;
	kind: string;
	key: string;
	severity: 'info' | 'warning' | 'critical';
	title: string;
	message: string;
	link?: string;
	labels?: Record<string, string>;
	firstSeen: string;
	lastSeen: string;
}
