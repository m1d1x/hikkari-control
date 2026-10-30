<script lang="ts">
	import EChart from './EChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';

	interface Props {
		note?: string;
		height?: string;
	}
	let { note = 'нет измерений за это окно', height = '200px' }: Props = $props();
	const option: EChartsOption = {
		grid: { left: 8, right: 12, top: 16, bottom: 24, containLabel: true },
		xAxis: { type: 'category', data: [], axisLine: { lineStyle: { color: '#27272a' } } },
		yAxis: {
			type: 'value',
			min: 0,
			max: 1,
			axisLabel: { color: '#71717a' },
			splitLine: { lineStyle: { color: '#27272a' } },
		},
		series: [{ type: 'line', data: [] }],
	};
</script>

<div class="empty-chart">
	<EChart {option} {height} />
	<p role="status">{note}</p>
</div>

<style>
	.empty-chart {
		width: 100%;
	}
	p {
		margin: 4px 0 0;
		text-align: center;
		font-size: 12px;
		color: var(--text-muted);
	}
</style>
