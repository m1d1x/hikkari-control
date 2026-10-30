<script lang="ts">
	import EChart from '$components/charts/EChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';

	/** One line or column stack on the chart. */
	export interface Series {
		name: string;
		/** A hex colour from `./colors`: the gradients append an alpha suffix. */
		color: string;
		/** One value per label; null is a gap, not a zero. */
		vals: (number | null)[];
		type?: 'line' | 'bar';
		/** Fills under a line, for the series the eye should follow first. */
		area?: boolean;
		/** Bars with the same stack id pile up instead of standing apart. */
		stack?: string;
		/** Appended to the value in the tooltip, e.g. " ч". */
		unit?: string;
	}

	/**
	 * The section's one time-series chart. Every screen draws its history
	 * through this component so the axes, the grid, the tooltip and the legend
	 * are the same everywhere, and so a new metric costs a series definition
	 * rather than another 200-line chart file.
	 *
	 * It draws what it is given and nothing else: no smoothing of missing
	 * points into a line, no invented buckets. Empty charts retain their axes
	 * and show a note so missing measurements remain distinct from zero.
	 */
	interface Props {
		labels: string[];
		series: Series[];
		height?: string;
		/** Shown below the axes when there is nothing to draw. */
		emptyNote?: string;
		/** Vertical markers: a pause, a rollout, a policy change. */
		marks?: { at: number; label: string }[];
		/** Keeps the value axis honest for shares. */
		max?: number | null;
		/** Draws the value axis; off for a sparkline-sized chart. */
		showAxis?: boolean;
	}

	let {
		labels,
		series,
		height = '200px',
		emptyNote = 'нет данных за это окно',
		marks = [],
		max = null,
		showAxis = true,
	}: Props = $props();

	const hasData = $derived(labels.length > 0 && series.some((s) => s.vals.some((v) => v !== null && v !== undefined)));

	// A series measured only on the days something happened is mostly gaps.
	// Drawn as a bare line it reads as a broken chart, so a sparse line joins
	// its points and marks each one — the dots say where a measurement
	// actually is, and the segment between them is plainly an interpolation.
	const hasBars = $derived(series.some((s) => s.type === 'bar'));

	const sparse = $derived.by(() => {
		const counts = series
			.filter((s) => (s.type ?? 'line') === 'line')
			.map((s) => s.vals.filter((v) => v !== null && v !== undefined).length);
		return counts.length > 0 && Math.max(...counts) <= 12;
	});

	const option: EChartsOption = $derived.by(() => ({
		tooltip: {
			trigger: 'axis',
			// Columns are read by the band they stand in, not by a line
			// through one of them: with bars the pointer is the whole slot,
			// so hovering anywhere in it gives every series of that bucket.
			axisPointer: hasBars
				? { type: 'shadow', shadowStyle: { color: '#ffffff0d' } }
				: { type: 'line', lineStyle: { color: '#52525b', type: 'dashed' } },
			backgroundColor: '#18181b',
			borderColor: '#27272a',
			padding: [8, 12],
			textStyle: { color: '#f3f4f6', fontSize: 12 },
			formatter: (params: any) => {
				if (!Array.isArray(params) || params.length === 0) return '';
				const idx = params[0].dataIndex;
				let html = `<div style="font-weight:600;margin-bottom:6px;color:#a1a1aa;font-size:11px">${labels[idx] ?? ''}</div>`;
				for (const p of params) {
					if (p.value === null || p.value === undefined) continue;
					const unit = series.find((s) => s.name === p.seriesName)?.unit ?? '';
					const marker = `<span style="display:inline-block;margin-right:6px;border-radius:50%;width:8px;height:8px;background-color:${p.color}"></span>`;
					html += `<div style="display:flex;justify-content:space-between;gap:20px;font-size:12px;margin-top:2px">
						<span style="color:#d4d4d8">${marker}${p.seriesName}</span>
						<span style="font-family:monospace;font-weight:600">${Number(p.value).toLocaleString('ru-RU')}${unit}</span>
					</div>`;
				}
				return html;
			},
		},
		legend: {
			show: series.length > 1,
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
			top: series.length > 1 ? 30 : 12,
			bottom: 4,
			containLabel: true,
		},
		xAxis: {
			type: 'category',
			data: labels,
			boundaryGap: hasBars,
			axisLine: { lineStyle: { color: '#27272a' } },
			axisTick: { show: false },
			axisLabel: {
				color: '#71717a',
				fontSize: 11,
				interval: Math.max(0, Math.floor(labels.length / 6) - 1),
			},
		},
		yAxis: {
			type: 'value',
			show: showAxis,
			min: 0,
			max: max ?? undefined,
			splitLine: { lineStyle: { color: '#1f1f23', type: 'dashed' } },
			axisLabel: { color: '#71717a', fontSize: 10 },
		},
		series: series.map((s, i) => ({
			name: s.name,
			type: s.type ?? 'line',
			smooth: s.type === 'bar' ? undefined : !sparse,
			showSymbol: sparse,
			symbol: 'circle',
			symbolSize: 6,
			stack: s.stack,
			barMaxWidth: 18,
			data: s.vals,
			connectNulls: sparse,
			lineStyle: { width: 2.2, color: s.color },
			itemStyle: { color: s.color, borderRadius: s.type === 'bar' ? [2, 2, 0, 0] : 0 },
			areaStyle: s.area
				? {
						color: {
							type: 'linear',
							x: 0,
							y: 0,
							x2: 0,
							y2: 1,
							colorStops: [
								{ offset: 0, color: `${s.color}38` },
								{ offset: 1, color: `${s.color}03` },
							],
						},
					}
				: undefined,
			markLine:
				i === 0 && marks.length > 0
					? {
							symbol: 'none',
							silent: true,
							label: {
								show: true,
								formatter: (p: any) => marks[p.dataIndex]?.label ?? '',
								color: '#a1a1aa',
								fontSize: 10,
								position: 'insideEndTop',
							},
							lineStyle: { color: '#71717a', type: 'dashed', width: 1 },
							data: marks.map((m) => ({ xAxis: m.at })),
						}
					: undefined,
		})),
	}));
</script>

<div class="ts">
	<EChart {option} {height} />
	{#if !hasData}<p class="ts-empty">{emptyNote}</p>{/if}
</div>

<style>
	.ts {
		width: 100%;
		position: relative;
	}

	.ts-empty {
		display: flex;
		align-items: center;
		justify-content: center;
		margin: 4px 0 0;
		font-size: 12px;
		color: var(--text-3);
	}
</style>
