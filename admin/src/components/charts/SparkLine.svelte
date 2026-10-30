<script lang="ts">
	import EChart from './EChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';

	interface Props {
		data: number[];
		color?: string;
		height?: string;
		width?: string;
	}

	let { data, color = '#e8699a', height = '32px', width = '80px' }: Props = $props();

	let option: EChartsOption = $derived({
		grid: { left: 0, right: 0, top: 2, bottom: 2 },
		xAxis: { type: 'category', show: false, data: data.map((_, i) => i) },
		yAxis: { type: 'value', show: false },
		series: [
			{
				type: 'line',
				data,
				smooth: true,
				symbol: 'none',
				lineStyle: { width: 1.5, color },
				areaStyle: { opacity: 0.1, color },
			},
		],
	});
</script>

<div
	class="sparkline"
	class:empty={data.length === 0}
	title={data.length === 0 ? 'Нет измерений' : undefined}
	style="width: {width}; height: {height}"
>
	<EChart {option} {height} />
</div>

<style>
	.empty {
		border-bottom: 1px dashed var(--border);
	}

	.sparkline {
		display: inline-flex;
	}
</style>
