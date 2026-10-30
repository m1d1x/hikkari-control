<script lang="ts">
	import EChart from './EChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';
	import type { ProbeBucket } from '$lib/types/system';

	// One latency-over-time area chart for a probe target. Smooth line +
	// translucent fill so a row of these reads as "waves" rather than a
	// dense grid of segments. Buckets where error_count > 0 get a red
	// markPoint on top so flaps stay visible even when latency itself
	// stays flat. The line color reflects the worst status seen across
	// the visible window so a single glance tells you "all green" vs.
	// "had issues" without hovering.

	interface Props {
		buckets: ProbeBucket[];
		label?: string;
		bucketSeconds?: number;
	}

	let { buckets, label = '', bucketSeconds = 0 }: Props = $props();

	const COLOR_OK = '#3fb27f';
	const COLOR_DEGRADED = '#e6a23c';
	const COLOR_ERROR = '#e85b5b';
	const COLOR_UNKNOWN = '#7a8290';

	function severity(status: string): number {
		if (status === 'error' || status === 'timeout') return 4;
		if (status === 'degraded') return 2;
		if (status === 'unknown') return 1;
		return 0;
	}

	function statusColor(status: string): string {
		if (status === 'ok') return COLOR_OK;
		if (status === 'degraded') return COLOR_DEGRADED;
		if (status === 'unknown') return COLOR_UNKNOWN;
		return COLOR_ERROR;
	}

	// Worst status across the window decides the line color. Mirrors the
	// behavior of the legacy uptime bar (one bad sample paints the row).
	let worstStatus = $derived.by(() => {
		let worst = 'ok';
		for (const b of buckets) {
			if (severity(b.status) > severity(worst)) worst = b.status;
		}
		return worst;
	});

	let lineColor = $derived(statusColor(worstStatus));

	let uptimePct = $derived.by(() => {
		if (buckets.length === 0) return null;
		let totalSamples = 0;
		let badSamples = 0;
		for (const b of buckets) {
			totalSamples += b.n;
			badSamples += b.error_count;
		}
		if (totalSamples === 0) return 100;
		const pct = (1 - badSamples / totalSamples) * 100;
		return Math.round(pct * 100) / 100;
	});

	function formatBucket(sec: number): string {
		if (sec >= 3600) return `${sec / 3600}h`;
		if (sec >= 60) return `${sec / 60}m`;
		return sec === 0 ? '10s' : `${sec}s`;
	}

	function formatTime(iso: string): string {
		return new Date(iso).toLocaleTimeString('ru-RU', {
			hour: '2-digit',
			minute: '2-digit',
		});
	}

	// Status-bar segments under the chart. Worst-of-window downsampling so
	// flap density stays visible at any zoom — different signal from the
	// chart (which encodes magnitude). Capped at ~144 segments to keep DOM
	// trivial and matches the chart's effective resolution.
	const MAX_SEGMENTS = 144;
	let segments = $derived.by(() => {
		if (buckets.length === 0) return [];
		if (buckets.length <= MAX_SEGMENTS) return buckets;
		const step = buckets.length / MAX_SEGMENTS;
		const result: ProbeBucket[] = [];
		for (let i = 0; i < MAX_SEGMENTS; i++) {
			const start = Math.floor(i * step);
			const end = Math.min(buckets.length, Math.floor((i + 1) * step));
			let worst = buckets[start];
			for (let j = start + 1; j < end; j++) {
				if (severity(buckets[j].status) > severity(worst.status)) worst = buckets[j];
			}
			result.push(worst);
		}
		return result;
	});

	function segmentTooltip(b: ProbeBucket): string {
		const flap = b.error_count > 0 ? `, ${b.error_count} fail` : '';
		return `${formatTime(b.ts)} — ${b.status.toUpperCase()} · ${b.latency_ms}ms${flap}`;
	}

	let option: EChartsOption = $derived.by(() => {
		const xs = buckets.map((b) => formatTime(b.ts));
		const ys = buckets.map((b) => b.latency_ms);

		// Failure markers: only buckets where at least one sample failed.
		// Tagged with the bucket index so tooltip can pull the right row.
		const failPoints = buckets
			.map((b, i) => ({ b, i }))
			.filter(({ b }) => b.error_count > 0)
			.map(({ b, i }) => ({
				name: '',
				xAxis: i,
				yAxis: b.latency_ms,
				symbolSize: 8,
				itemStyle: { color: COLOR_ERROR, borderColor: '#1a1a1a', borderWidth: 1 },
			}));

		const winLabel = formatBucket(bucketSeconds);

		return {
			grid: { left: 0, right: 0, top: 6, bottom: 0 },
			tooltip: {
				trigger: 'axis',
				backgroundColor: 'rgba(20, 20, 20, 0.92)',
				borderWidth: 0,
				textStyle: { color: '#eaeaea', fontSize: 11 },
				formatter: (params: any) => {
					const p = Array.isArray(params) ? params[0] : params;
					const idx = p.dataIndex as number;
					const b = buckets[idx];
					if (!b) return '';
					const flap =
						b.error_count > 0 ? `<br/><span style="color:${COLOR_ERROR}">⚠ ${b.error_count} fail / ${b.n}</span>` : '';
					return `${formatTime(b.ts)} · ${winLabel}<br/>` + `<b>${b.latency_ms} ms</b> · ${b.status}` + flap;
				},
			},
			xAxis: {
				type: 'category',
				show: false,
				boundaryGap: false,
				data: xs,
			},
			yAxis: {
				type: 'value',
				show: false,
				min: 0,
			},
			series: [
				{
					type: 'line',
					data: ys,
					smooth: true,
					symbol: 'none',
					sampling: 'lttb', // keep visual fidelity at high point counts
					lineStyle: { width: 1.6, color: lineColor },
					areaStyle: {
						opacity: 0.18,
						color: lineColor,
					},
					markPoint: failPoints.length > 0 ? { data: failPoints } : undefined,
				},
			],
		};
	});
</script>

<div class="probe-track">
	{#if label}
		<div class="track-label mono">{label}</div>
	{/if}
	<div class="chart-row">
		<div class="chart-stack">
			{#if buckets.length === 0}
				<span class="empty-text">no probe data yet</span>
			{:else}
				<div class="chart-area">
					<EChart {option} height="48px" />
				</div>
				<div class="status-bar">
					{#each segments as seg (seg.ts)}
						<div class="seg" style="background: {statusColor(seg.status)}" title={segmentTooltip(seg)}></div>
					{/each}
				</div>
			{/if}
		</div>
		{#if uptimePct !== null}
			<span class="uptime-pct mono">{uptimePct}%</span>
		{/if}
	</div>
</div>

<style>
	.probe-track {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.track-label {
		font-size: 10px;
		font-weight: 600;
		letter-spacing: 0.08em;
		color: var(--text-muted);
		text-transform: uppercase;
	}

	.chart-row {
		display: flex;
		align-items: center;
		gap: 12px;
		min-width: 0;
	}

	.chart-stack {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.chart-area {
		min-height: 48px;
	}

	.status-bar {
		display: flex;
		gap: 1px;
		height: 6px;
		border-radius: 2px;
		overflow: hidden;
		background: var(--bg-raised);
	}

	.seg {
		flex: 1;
		min-width: 1px;
		transition: opacity var(--transition-fast, 150ms);
	}

	.seg:hover {
		opacity: 0.6;
	}

	.empty-text {
		font-size: 11px;
		color: var(--text-muted);
		opacity: 0.5;
	}

	.uptime-pct {
		font-size: 12px;
		font-weight: 600;
		color: var(--text-primary);
		white-space: nowrap;
		min-width: 56px;
		text-align: right;
	}
</style>
