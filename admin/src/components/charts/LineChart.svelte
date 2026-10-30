<script lang="ts">
	import EChart from './EChart.svelte';
	import { chartColors } from '$lib/utils/colors';
	import type { EChartsOption } from '$lib/utils/echarts';

	interface Series {
		name: string;
		/** null is a gap (no sample), not a zero. */
		data: (number | null)[];
		type?: 'line' | 'area';
	}

	interface Props {
		labels: string[];
		series: Series[];
		height?: string;
		smooth?: boolean;
		showLegend?: boolean;
	}

	let { labels, series, height = '300px', smooth = true, showLegend = true }: Props = $props();

	let option: EChartsOption = $derived({
		tooltip: {
			trigger: 'axis',
		},
		legend: showLegend && series.length > 1 ? { show: true, bottom: 0 } : undefined,
		grid: {
			left: 48,
			right: 16,
			top: 16,
			bottom: showLegend && series.length > 1 ? 40 : 24,
			containLabel: false,
		},
		xAxis: {
			type: 'category',
			data: labels,
			boundaryGap: false,
		},
		yAxis: {
			type: 'value',
		},
		series: series.map((s, i) => ({
			name: s.name,
			type: 'line',
			data: s.data,
			smooth,
			symbol: 'none',
			lineStyle: { width: 2 },
			areaStyle: s.type === 'area' ? { opacity: 0.08 } : undefined,
			color: chartColors[i % chartColors.length],
		})),
	});
</script>

<EChart {option} {height} />
