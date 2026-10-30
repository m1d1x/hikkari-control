<script lang="ts">
	import EChart from '$components/charts/EChart.svelte';
	import { formatBytes } from '$lib/utils/format';
	import { statusColors } from '$lib/utils/colors';
	import type { EChartsOption } from '$lib/utils/echarts';
	import type { SystemMetrics } from '$lib/types/system';

	interface MetricsSnapshot {
		ts: string;
		cpuPercent: number;
		memUsed: number;
		memTotal: number;
		memPercent: number;
		diskUsed: number;
		diskTotal: number;
		diskPercent: number;
	}

	interface Props {
		metrics: SystemMetrics;
		history: MetricsSnapshot[];
	}

	let { metrics, history }: Props = $props();

	function levelColor(pct: number): string {
		if (pct >= 85) return statusColors.critical;
		if (pct >= 60) return statusColors.warning;
		return statusColors.healthy;
	}

	function formatTime(ts: string): string {
		return new Date(ts).toLocaleTimeString('ru-RU', {
			hour: '2-digit',
			minute: '2-digit',
		});
	}

	function makeChartOption(data: number[], color: string, max: number, unit: string = '%'): EChartsOption {
		const labels = history.map((p) => formatTime(p.ts));
		const labelInterval = Math.max(Math.floor(labels.length / 8) - 1, 0);

		return {
			grid: { left: 52, right: 16, top: 12, bottom: 28 },
			xAxis: {
				type: 'category',
				data: labels,
				axisLabel: {
					show: true,
					fontSize: 11,
					color: '#787878',
					interval: labelInterval,
				},
				axisLine: { lineStyle: { color: '#3a3a3a' } },
				axisTick: { show: false },
			},
			yAxis: {
				type: 'value',
				min: 0,
				max,
				axisLabel: {
					fontSize: 11,
					color: '#787878',
					formatter: (v: number) => `${v}${unit}`,
				},
				splitLine: { lineStyle: { color: '#2c2c2c' } },
			},
			tooltip: {
				trigger: 'axis',
				backgroundColor: '#262626',
				borderColor: '#3a3a3a',
				textStyle: { color: '#ededed', fontSize: 12 },
			},
			series: [
				{
					type: 'line',
					data,
					smooth: false,
					symbol: 'none',
					lineStyle: { width: 1.5, color },
					areaStyle: { opacity: 0.08, color },
				},
			],
		};
	}

	let cpuOption = $derived(
		makeChartOption(
			history.map((p) => Math.round(p.cpuPercent * 10) / 10),
			levelColor(metrics.cpuPercent),
			100,
		),
	);

	let memOption = $derived(
		makeChartOption(
			history.map((p) => Math.round(p.memPercent * 10) / 10),
			levelColor(metrics.memPercent),
			100,
		),
	);

	let diskOption = $derived(
		makeChartOption(
			history.map((p) => p.diskPercent ?? metrics.diskPercent),
			levelColor(metrics.diskPercent),
			100,
		),
	);
</script>

<div class="section">
	<h3 class="section-title">VPS Resources</h3>

	<!-- CPU -->
	<div class="metric-card">
		<div class="metric-header">
			<span class="metric-label">CPU</span>
			<span class="metric-value mono" style="color: {levelColor(metrics.cpuPercent)}">
				{metrics.cpuPercent.toFixed(1)}%
			</span>
		</div>
		<div class="metric-bar-track">
			<div
				class="metric-bar-fill"
				style="width: {metrics.cpuPercent}%; background: {levelColor(metrics.cpuPercent)}"
			></div>
		</div>
		<div class="metric-chart">
			<EChart option={cpuOption} height="220px" />
		</div>
	</div>

	<!-- RAM -->
	<div class="metric-card">
		<div class="metric-header">
			<span class="metric-label">RAM</span>
			<span class="metric-value mono" style="color: {levelColor(metrics.memPercent)}">
				{formatBytes(metrics.memUsed)} / {formatBytes(metrics.memTotal)}
				<span class="metric-pct">({metrics.memPercent.toFixed(1)}%)</span>
			</span>
		</div>
		<div class="metric-bar-track">
			<div
				class="metric-bar-fill"
				style="width: {metrics.memPercent}%; background: {levelColor(metrics.memPercent)}"
			></div>
		</div>
		<div class="metric-chart">
			<EChart option={memOption} height="220px" />
		</div>
	</div>

	<!-- Disk -->
	<div class="metric-card">
		<div class="metric-header">
			<span class="metric-label">Disk</span>
			<span class="metric-value mono" style="color: {levelColor(metrics.diskPercent)}">
				{formatBytes(metrics.diskUsed)} / {formatBytes(metrics.diskTotal)}
				<span class="metric-pct">({metrics.diskPercent.toFixed(1)}%)</span>
			</span>
		</div>
		<div class="metric-bar-track">
			<div
				class="metric-bar-fill"
				style="width: {metrics.diskPercent}%; background: {levelColor(metrics.diskPercent)}"
			></div>
		</div>
		<div class="metric-chart">
			<EChart option={diskOption} height="220px" />
		</div>
	</div>
</div>

<style>
	.section {
		margin-bottom: 24px;
	}

	.section-title {
		font-size: 14px;
		font-weight: 600;
		color: var(--text-secondary);
		margin-bottom: 12px;
	}

	.metric-card {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		padding: var(--card-padding);
		margin-bottom: var(--card-gap);
	}

	.metric-card:last-child {
		margin-bottom: 0;
	}

	.metric-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 8px;
	}

	.metric-label {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.metric-value {
		font-size: 13px;
		font-weight: 600;
	}

	.metric-pct {
		font-size: 11px;
		opacity: 0.7;
	}

	.metric-bar-track {
		height: 4px;
		background: var(--bg-raised);
		border-radius: 2px;
		overflow: hidden;
	}

	.metric-bar-fill {
		height: 100%;
		border-radius: 2px;
		transition: width 0.3s ease;
	}

	.metric-chart {
		margin-top: 12px;
	}
</style>
