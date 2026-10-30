<script lang="ts">
	import EChart from './EChart.svelte';
	import { chartColors } from '$lib/utils/colors';
	import type { EChartsOption } from '$lib/utils/echarts';

	interface Series {
		name: string;
		data: number[];
	}

	interface Props {
		labels: string[];
		series: Series[];
		height?: string;
		stacked?: boolean;
		horizontal?: boolean;
		showLegend?: boolean;
	}

	let { labels, series, height = '300px', stacked = false, horizontal = false, showLegend = true }: Props = $props();

	let option: EChartsOption = $derived({
		tooltip: {
			trigger: 'axis',
			axisPointer: { type: 'shadow' },
		},
		legend: showLegend && series.length > 1 ? { show: true, bottom: 0 } : undefined,
		grid: {
			left: 12,
			right: 16,
			top: 16,
			bottom: showLegend && series.length > 1 ? 40 : 24,
			containLabel: true,
		},
		xAxis: horizontal ? { type: 'value' } : { type: 'category', data: labels },
		yAxis: horizontal ? { type: 'category', data: labels } : { type: 'value' },
		series: series.map((s, i) => ({
			name: s.name,
			type: 'bar',
			data: s.data,
			stack: stacked ? 'total' : undefined,
			barMaxWidth: 40,
			itemStyle: {
				borderRadius: stacked ? 0 : [3, 3, 0, 0],
				color: chartColors[i % chartColors.length],
			},
		})),
	});
</script>

<EChart {option} {height} />
