<script lang="ts">
	import EChart from './EChart.svelte';
	import { chartColors } from '$lib/utils/colors';
	import type { EChartsOption } from '$lib/utils/echarts';

	interface FunnelStep {
		name: string;
		value: number;
	}

	interface Props {
		steps: FunnelStep[];
		height?: string;
	}

	let { steps, height = '300px' }: Props = $props();

	let option: EChartsOption = $derived({
		tooltip: {
			trigger: 'item',
			formatter: (p: any) =>
				`${p.name}: ${p.value.toLocaleString()} (${((p.value / steps[0].value) * 100).toFixed(1)}%)`,
		},
		series: [
			{
				type: 'funnel',
				left: 60,
				right: 60,
				top: 16,
				bottom: 16,
				minSize: '10%',
				maxSize: '100%',
				sort: 'descending',
				gap: 4,
				label: {
					show: true,
					position: 'inside',
					formatter: '{b}\n{c}',
					fontSize: 12,
					color: '#ededed',
				},
				itemStyle: {
					borderWidth: 0,
				},
				data: steps.map((s, i) => ({
					name: s.name,
					value: s.value,
					itemStyle: { color: chartColors[i % chartColors.length] },
				})),
			},
		],
	});
</script>

<EChart {option} {height} />
