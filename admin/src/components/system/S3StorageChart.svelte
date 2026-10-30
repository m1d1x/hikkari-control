<script lang="ts">
	import EChart from '$components/charts/EChart.svelte';
	import { formatBytes } from '$lib/utils/format';
	import { chartColors } from '$lib/utils/colors';
	import type { EChartsOption } from '$lib/utils/echarts';
	import type { S3StoragePoint } from '$lib/types/system';

	interface Props {
		history: S3StoragePoint[];
	}

	let { history }: Props = $props();

	let latest = $derived(history.at(-1));

	let option: EChartsOption = $derived.by(() => {
		const values = history.map((p) => p.usedBytes);
		const minVal = Math.min(...values);
		const maxVal = Math.max(...values);

		// Padding so flat line renders in the middle, not at edge
		const range = maxVal - minVal;
		const padding = range === 0 ? Math.max(maxVal * 0.15, 1024 * 1024) : range * 0.1;

		const labels = history.map((p) =>
			new Date(p.ts).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' }),
		);

		return {
			grid: { left: 56, right: 16, top: 12, bottom: 28 },
			xAxis: {
				type: 'category',
				data: labels,
				axisLabel: {
					show: true,
					fontSize: 11,
					color: '#787878',
					interval: Math.max(Math.floor(labels.length / 8) - 1, 0),
				},
				axisLine: { lineStyle: { color: '#3a3a3a' } },
				axisTick: { show: false },
			},
			yAxis: {
				type: 'value',
				min: Math.max(0, minVal - padding),
				max: maxVal + padding,
				axisLabel: {
					formatter: (v: number) => formatBytes(v),
					fontSize: 10,
					color: '#787878',
				},
				splitLine: { lineStyle: { color: '#2c2c2c' } },
			},
			tooltip: {
				trigger: 'axis',
				backgroundColor: '#262626',
				borderColor: '#3a3a3a',
				textStyle: { color: '#ededed', fontSize: 12 },
				formatter: (params: any) => {
					const p = params[0];
					const point = history[p.dataIndex];
					const date = new Date(point.ts);
					const time = date.toLocaleTimeString('ru-RU', {
						hour: '2-digit',
						minute: '2-digit',
					});
					return `${time}<br/>Storage: <b>${formatBytes(point.usedBytes)}</b><br/>Objects: <b>${point.objects.toLocaleString()}</b>`;
				},
			},
			series: [
				{
					type: 'line',
					data: values,
					smooth: false,
					symbol: 'none',
					lineStyle: { width: 1.5, color: chartColors[2] },
					areaStyle: { opacity: 0.08, color: chartColors[2] },
				},
			],
		};
	});
</script>

<div class="section">
	<h3 class="section-title">S3 Storage</h3>
	<div class="s3-card">
		<div class="s3-header">
			{#if latest}
				<div class="s3-stat">
					<span class="s3-label">Used</span>
					<span class="s3-value mono">{formatBytes(latest.usedBytes)}</span>
				</div>
				<div class="s3-stat">
					<span class="s3-label">Objects</span>
					<span class="s3-value mono">{latest.objects.toLocaleString()}</span>
				</div>
			{/if}
		</div>
		<div class="s3-chart">
			<EChart {option} height="180px" />
		</div>
	</div>
</div>

<style>
	.section {
		display: flex;
		flex-direction: column;
	}

	.section-title {
		font-size: 14px;
		font-weight: 600;
		color: var(--text-secondary);
		margin-bottom: 12px;
	}

	.s3-card {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		padding: var(--card-padding);
		flex: 1;
		display: flex;
		flex-direction: column;
	}

	.s3-header {
		display: flex;
		gap: 24px;
		margin-bottom: 12px;
	}

	.s3-stat {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.s3-label {
		font-size: 11px;
		color: var(--text-muted);
	}

	.s3-value {
		font-size: 14px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.s3-chart {
		border-top: 1px solid var(--border-subtle);
		padding-top: 8px;
		flex: 1;
	}
</style>
