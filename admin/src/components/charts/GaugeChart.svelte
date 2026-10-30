<script lang="ts">
	import EChart from './EChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';

	interface Props {
		value: number;
		max?: number;
		label?: string;
		color?: string;
		height?: string;
	}

	let { value, max = 100, label = '', color = '#e8699a', height = '200px' }: Props = $props();

	let option: EChartsOption = $derived({
		series: [
			{
				type: 'gauge',
				center: ['50%', '65%'],
				radius: '90%',
				startAngle: 200,
				endAngle: -20,
				min: 0,
				max,
				progress: {
					show: true,
					width: 14,
					itemStyle: { color },
				},
				axisLine: {
					lineStyle: { width: 14, color: [[1, '#2c2c2c']] },
				},
				axisTick: { show: false },
				splitLine: { show: false },
				axisLabel: { show: false },
				pointer: { show: false },
				title: {
					show: !!label,
					offsetCenter: [0, '35%'],
					fontSize: 12,
					color: '#7e7e7e',
				},
				detail: {
					valueAnimation: true,
					offsetCenter: [0, '5%'],
					fontSize: 24,
					fontWeight: 600,
					fontFamily: "'IBM Plex Mono', monospace",
					color: '#ededed',
					formatter: '{value}',
				},
				data: [{ value, name: label }],
			},
		],
	});
</script>

<EChart {option} {height} />
