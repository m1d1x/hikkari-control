<script lang="ts">
	import StatusBadge from '$components/data/StatusBadge.svelte';
	import UptimeBar from '$components/charts/UptimeBar.svelte';
	import ProbeBar from '$components/charts/ProbeBar.svelte';
	import SparkLine from '$components/charts/SparkLine.svelte';
	import MetricChip from '$components/system/MetricChip.svelte';
	import ServiceLogs from '$components/system/ServiceLogs.svelte';
	import type { ServiceHealth, UptimePoint, ThresholdResult, ProbeBucket, ActiveOpsAlert } from '$lib/types/system';

	interface Props {
		service: ServiceHealth;
		uptimeChecks: UptimePoint[];
		uptimePct: number | null;
		latencySpark?: number[];
		thresholds?: ThresholdResult[];
		// CH-backed probe history (admin-server). When present these supersede
		// the in-memory uptimeChecks fallback and we render the new dual-track
		// layout (INTERNAL / EXTERNAL). Either prop may be empty independently
		// — internal-only services like Postgres/CH have no external row.
		internalProbes?: ProbeBucket[];
		externalProbes?: ProbeBucket[];
		bucketSeconds?: number;
		// Active alerts already filtered to this service by the parent.
		// Non-empty means Telegram is currently paging about this card —
		// render a loud banner above the probe tracks so the dashboard
		// matches the alert.
		opsAlerts?: ActiveOpsAlert[];
	}

	let {
		service,
		uptimeChecks,
		uptimePct,
		latencySpark = [],
		thresholds = [],
		internalProbes = [],
		externalProbes = [],
		bucketSeconds = 0,
		opsAlerts = [],
	}: Props = $props();

	let topAlertSeverity = $derived(
		opsAlerts.some((a) => a.severity === 'critical')
			? 'critical'
			: opsAlerts.some((a) => a.severity === 'warning')
				? 'warning'
				: 'info',
	);

	const metricLabels: Record<string, string> = {
		connections: 'Connections',
		dbSize: 'DB Size',
		cacheHitRate: 'Cache Hit',
		txTotal: 'Transactions',
		memory: 'Memory',
		keys: 'Keys',
		opsPerSec: 'Ops/sec',
		hitRate: 'Hit Rate',
		connectedClients: 'Clients',
		evictedKeys: 'Evicted',
		totalEvents: 'Events',
		diskUsage: 'Disk',
		activeParts: 'Parts',
		eventsLastHour: 'Last Hour',
		inVoiceUsers: 'In voice',
		rooms: 'Rooms',
		bucket: 'Bucket',
		usedStorage: 'Used',
		objects: 'Objects',
	};

	// Metrics that have max counterparts (hidden from display)
	const maxMetricKeys = new Set(['maxConnections', 'maxMemory', 'maxClients']);

	// Map metric key → its max key
	const metricMaxMap: Record<string, string> = {
		connections: 'maxConnections',
		connectedClients: 'maxClients',
	};

	function getThresholdLevel(key: string): ThresholdResult['level'] {
		const t = thresholds.find((t) => t.metric === key && t.service === service.name.toLowerCase());
		return t?.level ?? 'ok';
	}

	let visibleMetrics = $derived(Object.entries(service.metrics).filter(([key]) => !maxMetricKeys.has(key)));

	function formatLatency(ms: number): string {
		if (ms < 1) return `${(ms * 1000).toFixed(0)}µs`;
		if (ms < 1000) return `${ms.toFixed(1)}ms`;
		return `${(ms / 1000).toFixed(2)}s`;
	}

	// Whether to use the new ProbeBar tracks (CH data present) or fall
	// back to the legacy in-memory UptimeBar. Legacy mode kicks in for
	// the first ~10s after admin-server restart while CH lags behind.
	let hasProbeData = $derived(internalProbes.length > 0 || externalProbes.length > 0);
</script>

<div
	class="service-row"
	class:has-alert={opsAlerts.length > 0}
	class:sev-critical={opsAlerts.length > 0 && topAlertSeverity === 'critical'}
	class:sev-warning={opsAlerts.length > 0 && topAlertSeverity === 'warning'}
>
	{#if opsAlerts.length > 0}
		<div class="alert-strip sev-{topAlertSeverity}">
			<span class="alert-dot"></span>
			<div class="alert-text">
				{#each opsAlerts as a (a.source + ':' + a.kind + ':' + a.key)}
					<div class="alert-line">
						<span class="alert-title">{a.title}</span>
						<span class="alert-msg">{a.message}</span>
					</div>
				{/each}
			</div>
		</div>
	{/if}
	<div class="service-main">
		<div class="service-info">
			<h3 class="service-name">{service.name}</h3>
			<StatusBadge status={service.status} />
			{#if service.latency > 0}
				<span class="latency mono">{formatLatency(service.latency)}</span>
			{/if}
		</div>
		{#if !hasProbeData}
			<div class="uptime-area">
				<UptimeBar checks={uptimeChecks} uptime={uptimePct} />
			</div>
		{/if}
	</div>

	{#if hasProbeData}
		<div class="probe-tracks">
			{#if internalProbes.length > 0}
				<ProbeBar buckets={internalProbes} label="Internal" {bucketSeconds} />
			{/if}
			{#if externalProbes.length > 0}
				<ProbeBar buckets={externalProbes} label="External" {bucketSeconds} />
			{/if}
		</div>
	{/if}

	<details class="extras">
		<summary>Details</summary>
		<div class="extras-body">
			<div class="latency-chart">
				<SparkLine data={latencySpark} color="#45b97c" height="40px" width="100%" />
			</div>
			{#if visibleMetrics.length > 0}
				<div class="service-metrics">
					{#each visibleMetrics as [key, val]}
						{@const maxKey = metricMaxMap[key]}
						{@const maxVal = maxKey ? String(service.metrics[maxKey] ?? '') : undefined}
						<MetricChip
							label={metricLabels[key] || key}
							value={String(val)}
							max={maxVal || undefined}
							level={getThresholdLevel(key)}
						/>
					{/each}
				</div>
			{/if}
			<ServiceLogs service={service.name.toLowerCase()} />
		</div>
	</details>
</div>

<style>
	.service-row {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		padding: var(--card-padding);
	}

	.service-row.has-alert.sev-critical {
		border-color: oklch(0.65 0.2 25 / 0.55);
		box-shadow: 0 0 0 1px oklch(0.65 0.2 25 / 0.25) inset;
	}

	.service-row.has-alert.sev-warning {
		border-color: oklch(0.78 0.12 85 / 0.55);
		box-shadow: 0 0 0 1px oklch(0.78 0.12 85 / 0.25) inset;
	}

	.alert-strip {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 8px 12px;
		margin: calc(-1 * var(--card-padding)) calc(-1 * var(--card-padding)) 12px;
		border-top-left-radius: var(--card-radius);
		border-top-right-radius: var(--card-radius);
		font-size: 12px;
	}

	.alert-strip.sev-critical {
		background: oklch(0.65 0.2 25 / 0.12);
		color: var(--text-primary);
		border-bottom: 1px solid oklch(0.65 0.2 25 / 0.3);
	}

	.alert-strip.sev-warning {
		background: oklch(0.78 0.12 85 / 0.12);
		color: var(--text-primary);
		border-bottom: 1px solid oklch(0.78 0.12 85 / 0.3);
	}

	.alert-strip.sev-info {
		background: var(--bg-raised);
		border-bottom: 1px solid var(--border-subtle);
	}

	.alert-dot {
		flex-shrink: 0;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		margin-top: 5px;
	}

	.sev-critical .alert-dot {
		background: var(--status-critical);
	}

	.sev-warning .alert-dot {
		background: var(--status-warning);
	}

	.sev-info .alert-dot {
		background: var(--status-unknown);
	}

	.alert-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.alert-line {
		display: flex;
		gap: 8px;
		align-items: baseline;
	}

	.alert-title {
		font-weight: 600;
		white-space: nowrap;
	}

	.alert-msg {
		color: var(--text-secondary);
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.service-main {
		display: flex;
		align-items: center;
		gap: 20px;
	}

	.service-info {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-shrink: 0;
		min-width: 240px;
	}

	.service-name {
		font-size: 15px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.latency {
		font-size: 12px;
		color: var(--text-muted);
	}

	.uptime-area {
		flex: 1;
		min-width: 0;
	}

	.probe-tracks {
		display: flex;
		flex-direction: column;
		gap: 10px;
		margin-top: 14px;
		padding-top: 14px;
		border-top: 1px solid var(--border-subtle);
	}

	.extras {
		margin-top: 12px;
		padding-top: 8px;
		border-top: 1px solid var(--border-subtle);
	}

	.extras > summary {
		cursor: pointer;
		font-size: 12px;
		font-weight: 500;
		color: var(--text-muted);
		padding: 4px 0;
		user-select: none;
		list-style: none;
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.extras > summary::before {
		content: '▸';
		font-size: 10px;
		transition: transform var(--transition-fast, 150ms);
		display: inline-block;
	}

	.extras[open] > summary::before {
		transform: rotate(90deg);
	}

	.extras > summary::-webkit-details-marker {
		display: none;
	}

	.extras > summary:hover {
		color: var(--text-primary);
	}

	.extras-body {
		margin-top: 10px;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.latency-chart {
		margin: 0 calc(-1 * var(--card-padding));
	}

	.service-metrics {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	@media (max-width: 900px) {
		.service-main {
			flex-direction: column;
			align-items: stretch;
		}

		.service-info {
			min-width: 0;
		}
	}
</style>
