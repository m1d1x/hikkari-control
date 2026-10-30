<script lang="ts">
	import EChart from './EChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';

	interface Props {
		xLabels: string[];
		yLabels: string[];
		data: [number, number, number][]; // [x, y, value]
		height?: string;
		min?: number;
		max?: number;
	}

	let { xLabels, yLabels, data, height = '300px', min = 0, max = 100 }: Props = $props();

	let option: EChartsOption = $derived({
		tooltip: {
			position: 'top',
			formatter: (p: any) => `${yLabels[p.value[1]]}, ${xLabels[p.value[0]]}: ${p.value[2]}`,
		},
		grid: {
			left: 80,
			right: 40,
			top: 8,
			bottom: 40,
		},
		xAxis: {
			type: 'category',
			data: xLabels,
			splitArea: { show: true },
			axisLabel: { fontSize: 11 },
		},
		yAxis: {
			type: 'category',
			data: yLabels,
			splitArea: { show: true },
			axisLabel: { fontSize: 11 },
		},
		visualMap: {
			min,
			max,
			calculable: true,
			orient: 'vertical',
			right: 0,
			top: 'center',
			itemHeight: 120,
			textStyle: { color: '#7e7e7e', fontSize: 10 },
			inRange: {
				color: ['#d4553e', '#c9a94e', '#45b97c'],
			},
		},
		series: [
			{
				type: 'heatmap',
				data,
				emphasis: {
					itemStyle: { shadowBlur: 10, shadowColor: 'rgba(0, 0, 0, 0.5)' },
				},
			},
		],
	});
</script>

<EChart {option} {height} />
