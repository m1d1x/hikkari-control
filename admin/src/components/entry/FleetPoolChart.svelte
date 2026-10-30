<script lang="ts">
	import EChart from '$components/charts/EChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';
	import { bucketLabel, bucketTitle, type FleetPoolPoint } from '$lib/api/entry';

	// The pool's size at the end of every bucket of the window, read off the
	// rows' own timestamps. Both lines are measured; nothing stands in for a
	// missing one.
	interface Props {
		series: FleetPoolPoint[];
		bucketSeconds: number;
		height?: string;
	}

	let { series, bucketSeconds, height = '75px' }: Props = $props();

	const labels = $derived(
		series.map((d, i) => (i === series.length - 1 ? 'сейчас' : bucketLabel(d.bucket, bucketSeconds))),
	);
	const relayVals = $derived(series.map((d) => d.size));
	const boxVals = $derived(series.map((d) => d.boxes));

	let option: EChartsOption = $derived.by(() => {
		return {
			tooltip: {
				trigger: 'axis',
				axisPointer: {
					type: 'line',
					lineStyle: { color: '#52525b', type: 'dashed' },
				},
				backgroundColor: '#18181b',
				borderColor: '#27272a',
				padding: [6, 10],
				textStyle: { color: '#f3f4f6', fontSize: 11 },
				formatter: (params: any) => {
					if (!Array.isArray(params) || params.length === 0) return '';
					const idx = params[0].dataIndex;
					const item = series[idx];
					if (!item) return '';
					let html = `<div style="font-size:10px;color:#a1a1aa;margin-bottom:4px">${bucketTitle(item.bucket, bucketSeconds)}</div>`;
					for (const p of params) {
						if (p.value === null || p.value === undefined) continue;
						const marker = `<span style="display:inline-block;margin-right:6px;border-radius:50%;width:7px;height:7px;background-color:${p.color}"></span>`;
						html += `<div style="display:flex;justify-content:space-between;gap:16px;font-size:11px;margin-top:2px">
							<span style="color:#d4d4d8">${marker}${p.seriesName}</span>
							<span style="font-family:monospace;font-weight:600">${Number(p.value).toLocaleString('ru-RU')}</span>
						</div>`;
					}
					return html;
				},
			},
			legend: {
				show: true,
				top: 0,
				right: 0,
				icon: 'circle',
				itemWidth: 7,
				itemHeight: 7,
				itemGap: 12,
				textStyle: { color: '#9ca3af', fontSize: 10 },
			},
			grid: {
				left: 4,
				right: 8,
				top: 18,
				bottom: 18,
				containLabel: false,
			},
			xAxis: {
				type: 'category',
				data: labels,
				boundaryGap: false,
				axisLine: { show: false },
				axisTick: { show: false },
				axisLabel: {
					color: '#52525b',
					fontSize: 10,
					interval: Math.max(0, Math.floor(series.length / 4) - 1),
				},
			},
			yAxis: {
				type: 'value',
				show: false,
				min: (value: any) => Math.max(0, value.min - 2),
			},
			series: [
				{
					name: 'Релеи',
					type: 'line',
					smooth: true,
					showSymbol: false,
					symbol: 'circle',
					symbolSize: 5,
					data: relayVals,
					lineStyle: { width: 2, color: '#10b981' },
					itemStyle: { color: '#10b981' },
					areaStyle: {
						color: {
							type: 'linear',
							x: 0,
							y: 0,
							x2: 0,
							y2: 1,
							colorStops: [
								{ offset: 0, color: 'rgba(16, 185, 129, 0.20)' },
								{ offset: 1, color: 'rgba(16, 185, 129, 0.01)' },
							],
						},
					},
				},
				{
					name: 'Машины',
					type: 'line',
					smooth: true,
					showSymbol: false,
					symbol: 'circle',
					symbolSize: 5,
					data: boxVals,
					lineStyle: { width: 1.8, color: '#06b6d4' },
					itemStyle: { color: '#06b6d4' },
				},
			],
		};
	});
</script>

<div class="pool-chart-container" style="height: {height}">
	<EChart {option} {height} />
</div>

<style>
	.pool-chart-container {
		width: 100%;
		position: relative;
	}
</style>
