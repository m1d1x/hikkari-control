<script lang="ts">
	import EChart from '$components/charts/EChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';
	import { bucketLabel, bucketTitle, type DeathRepBucket } from '$lib/api/entry';

	// Censor deaths against relays signed into the pool, bucket by bucket. A
	// relay handed back on purpose is not in the red line: the hub counts only
	// what the censor killed there.
	interface Props {
		series: DeathRepBucket[];
		bucketSeconds: number;
		height?: string;
	}

	let { series, bucketSeconds, height = '220px' }: Props = $props();

	const labels = $derived(series.map((p) => bucketLabel(p.bucket, bucketSeconds)));
	const repVals = $derived(series.map((p) => p.replacements));
	const deathVals = $derived(series.map((p) => p.deaths));

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
				padding: [8, 12],
				textStyle: { color: '#f3f4f6', fontSize: 12 },
				formatter: (params: any) => {
					if (!Array.isArray(params) || params.length === 0) return '';
					const item = series[params[0].dataIndex];
					if (!item) return '';
					let html = `<div style="font-weight:600;margin-bottom:6px;color:#a1a1aa;font-size:11px">${bucketTitle(item.bucket, bucketSeconds)}</div>`;
					for (const p of params) {
						if (p.value === null || p.value === undefined) continue;
						const marker = `<span style="display:inline-block;margin-right:6px;border-radius:50%;width:8px;height:8px;background-color:${p.color}"></span>`;
						html += `<div style="display:flex;justify-content:space-between;gap:20px;font-size:12px;margin-top:2px">
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
				itemWidth: 8,
				itemHeight: 8,
				itemGap: 16,
				textStyle: { color: '#9ca3af', fontSize: 11 },
			},
			grid: {
				left: 8,
				right: 12,
				top: 30,
				bottom: 4,
				containLabel: true,
			},
			xAxis: {
				type: 'category',
				data: labels,
				boundaryGap: false,
				axisLine: { lineStyle: { color: '#27272a' } },
				axisTick: { show: false },
				axisLabel: { color: '#71717a', fontSize: 11 },
			},
			yAxis: {
				type: 'value',
				splitLine: { lineStyle: { color: '#1f1f23', type: 'dashed' } },
				axisLabel: { color: '#71717a', fontSize: 10 },
			},
			series: [
				{
					name: 'Развороты',
					type: 'line',
					smooth: true,
					showSymbol: false,
					symbol: 'circle',
					symbolSize: 6,
					data: repVals,
					lineStyle: { width: 2.5, color: '#10b981' },
					itemStyle: { color: '#10b981' },
					areaStyle: {
						color: {
							type: 'linear',
							x: 0,
							y: 0,
							x2: 0,
							y2: 1,
							colorStops: [
								{ offset: 0, color: 'rgba(16, 185, 129, 0.22)' },
								{ offset: 1, color: 'rgba(16, 185, 129, 0.01)' },
							],
						},
					},
				},
				{
					name: 'Блокировки',
					type: 'line',
					smooth: true,
					showSymbol: false,
					symbol: 'circle',
					symbolSize: 6,
					data: deathVals,
					lineStyle: { width: 2.5, color: '#f43f5e' },
					itemStyle: { color: '#f43f5e' },
					areaStyle: {
						color: {
							type: 'linear',
							x: 0,
							y: 0,
							x2: 0,
							y2: 1,
							colorStops: [
								{ offset: 0, color: 'rgba(244, 63, 94, 0.20)' },
								{ offset: 1, color: 'rgba(244, 63, 94, 0.01)' },
							],
						},
					},
				},
			],
		};
	});
</script>

<div class="censor-timeline">
	<EChart {option} {height} />
</div>

<style>
	.censor-timeline {
		width: 100%;
		position: relative;
	}
</style>
