<script lang="ts">
	import EChart from '$components/charts/EChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';
	import type { ChartSet, ChartLine, SpendReading } from '$lib/api/entry';
	interface Props {
		set?: ChartSet;
		usesDomains?: boolean;
		moneyMode: boolean;
		spend?: SpendReading[];
	}
	let { set: reading, moneyMode, spend = [], usesDomains = false }: Props = $props();
	const emptyLine: ChartLine = { history: [], forecast: [] };
	const set: ChartSet = $derived(
		reading ?? {
			pool: '',
			loadMbps: emptyLine,
			machines: emptyLine,
			machinesCentsPerDay: emptyLine,
			...(usesDomains ? { domains: emptyLine, domainsCentsPerDay: emptyLine } : {}),
			machineMbps: 0,
			headroom: 0,
			events: [],
		},
	);
	function option(line: ChartLine, unit: string, color: string, cap?: number): EChartsOption {
		const data = (points: ChartLine['history']) =>
			points.map((p) => [Date.parse(p.at), unit === '$ / день' ? p.v / 100 : p.v]);
		return {
			tooltip: {
				trigger: 'axis',
				valueFormatter: (v: unknown) => `${Number(v).toLocaleString('ru-RU', { maximumFractionDigits: 2 })} ${unit}`,
			},
			legend: { data: ['история', 'прогноз'], textStyle: { color: '#9ca3af' } },
			grid: { left: 8, right: 18, top: 32, bottom: 24, containLabel: true },
			xAxis: {
				type: 'time',
				min: !line.history.length && !line.forecast.length ? Date.now() - 7 * 86400000 : undefined,
				max: !line.history.length && !line.forecast.length ? Date.now() + 86400000 : undefined,
				axisLabel: { color: '#9ca3af' },
			},
			yAxis: {
				type: 'value',
				min: 0,
				minInterval: unit === 'шт' ? 1 : undefined,
				axisLabel: { color: '#9ca3af' },
				splitLine: { lineStyle: { color: '#27272a' } },
			},
			series: [
				{
					name: 'история',
					type: 'line',
					step: unit === 'Мбит/с' ? false : 'end',
					showSymbol: false,
					data: data(line.history),
					lineStyle: { color, width: 2 },
					itemStyle: { color },
					...(cap !== undefined
						? {
								markLine: {
									symbol: 'none',
									data: [{ yAxis: cap / 100, name: 'лимит дня' }],
									label: { formatter: 'лимит дня', position: 'insideEndTop' },
									lineStyle: { color: '#f43f5e', type: 'dotted' },
								},
							}
						: {}),
				},
				{
					name: 'прогноз',
					type: 'line',
					showSymbol: false,
					data: data(line.forecast),
					lineStyle: { color: '#facc15', type: 'dashed', width: 2 },
					itemStyle: { color: '#facc15' },
				},
			],
		} as EChartsOption;
	}
	const charts = $derived([
		{ title: 'Суммарная нагрузка', line: set.loadMbps, unit: 'Мбит/с', color: '#06b6d4', cap: undefined },
		{
			title: moneyMode ? 'Аренда машин' : 'Машины',
			line: moneyMode ? set.machinesCentsPerDay : set.machines,
			unit: moneyMode ? '$ / день' : 'шт',
			color: '#a78bfa',
			cap: moneyMode ? spend.find((s) => s.role === 'hosting')?.dailyCapCents : undefined,
		},
		...(set.domains && set.domainsCentsPerDay
			? [
					{
						title: moneyMode ? 'Покупки доменов за 24 часа' : 'Домены в пуле и в работе',
						line: moneyMode ? set.domainsCentsPerDay : set.domains,
						unit: moneyMode ? '$ / день' : 'шт',
						color: '#10b981',
						cap: moneyMode ? spend.find((s) => s.role === 'registrar')?.dailyCapCents : undefined,
					},
				]
			: []),
	]);
</script>

<div class="charts">
	{#each charts as chart, i (i)}
		<div class="chart">
			<h3>{chart.title} <span>{chart.unit}</span></h3>
			{#key moneyMode}
				<EChart option={option(chart.line, chart.unit, chart.color, chart.cap)} height="210px" />
			{/key}
		</div>
	{/each}
</div>
{#if !set.domains}<p class="note">Этот протокол не использует домены из пула.</p>{/if}
{#if set.why}<p class="note">{set.why}</p>{/if}

<style>
	.charts {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));
		gap: 16px;
	}
	.chart {
		min-width: 0;
	}
	h3 {
		font-size: 12px;
		margin: 4px 0 12px;
		font-weight: 500;
	}
	span,
	.note {
		font-size: 11px;
		color: var(--text-3);
	}
	span {
		margin-left: 8px;
	}
</style>
