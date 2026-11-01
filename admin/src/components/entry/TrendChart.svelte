<script lang="ts">
	import { bucketLabel, bucketTitle } from '$lib/api/entry';

	/** One line on the chart. */
	export interface TrendSeries {
		key: string;
		label: string;
		/** any CSS colour; the line, the hover dot and the tooltip swatch */
		color: string;
		/** one value per bucket; null is a bucket nobody measured, drawn as a gap */
		values: (number | null)[];
		/** the value as the tooltip says it, unit included */
		format: (v: number) => string;
		/** the line the eye should follow first: thicker, with a soft fill under it */
		primary?: boolean;
	}

	/**
	 * The overview's line chart: several series over the window's buckets on
	 * one value axis. Money, traffic and survival all draw through it, so the
	 * three cards read the same way and share the screen's one time axis.
	 *
	 * It draws what it is given and nothing else. A bucket with no value
	 * breaks the line, a lone measured bucket between two gaps is a dot, and a
	 * chart with no value retains its axes and labels the missing measurements.
	 */
	interface Props {
		buckets: string[];
		bucketSeconds: number;
		series: TrendSeries[];
		/** the value axis label */
		formatAxis: (v: number) => string;
		height?: number;
		/** shown below the axes when there is nothing to draw */
		empty?: string;
		/** a fixed axis top, for a figure with a natural ceiling such as a percentage */
		max?: number;
	}

	let { buckets, bucketSeconds, series, formatAxis, height = 160, empty = 'нет данных за это окно', max }: Props = $props();

	let containerEl = $state<HTMLDivElement | null>(null);
	let containerWidth = $state<number>(850);
	let hoveredIdx = $state<number | null>(null);

	const PAD_L = 22;
	const PAD_R = 64;
	const PAD_T = 16;
	const PAD_B = 26;

	// One SVG unit is one pixel, so the type is never stretched.
	const W = $derived(Math.max(320, containerWidth || 850));
	const PW = $derived(W - PAD_L - PAD_R);
	const PH = $derived(height - PAD_T - PAD_B);

	const n = $derived(buckets.length);
	const hasData = $derived(series.some((s) => s.values.some((v) => v !== null && v !== undefined)));

	const x = (i: number) => (n < 2 ? PAD_L + PW / 2 : PAD_L + (i / (n - 1)) * PW);

	/** The axis top: the largest value with headroom, rounded to 1, 2 or 5 of a power of ten. */
	const yMax = $derived.by(() => {
		if (max !== undefined) return max;
		let top = 0;
		for (const s of series) for (const v of s.values) if (v !== null && v !== undefined && v > top) top = v;
		if (top <= 0) return 1;
		const raw = top * 1.15;
		const pow = 10 ** Math.floor(Math.log10(raw));
		for (const m of [1, 2, 2.5, 5, 10]) if (raw <= m * pow) return m * pow;
		return 10 * pow;
	});

	const y = (v: number) => PAD_T + (1 - Math.max(0, Math.min(yMax, v)) / yMax) * PH;

	/** A line broken wherever a bucket has no value. */
	function linePath(values: (number | null)[]): string {
		let d = '';
		let pen = false;
		values.forEach((v, i) => {
			if (v === null || v === undefined) {
				pen = false;
				return;
			}
			d += `${pen ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)} `;
			pen = true;
		});
		return d;
	}

	/** Buckets measured with no neighbour measured: invisible as a line, drawn as dots. */
	function lonePoints(values: (number | null)[]): number[] {
		const has = (i: number) => i >= 0 && i < values.length && values[i] !== null && values[i] !== undefined;
		return values.map((_, i) => i).filter((i) => has(i) && !has(i - 1) && !has(i + 1));
	}

	/** The soft fill under the primary line, one closed shape per unbroken run. */
	function areaPath(values: (number | null)[]): string {
		const runs: number[][] = [];
		let run: number[] = [];
		values.forEach((v, i) => {
			if (v === null || v === undefined) {
				if (run.length) runs.push(run);
				run = [];
			} else run.push(i);
		});
		if (run.length) runs.push(run);
		const base = y(0).toFixed(1);
		return runs
			.filter((r) => r.length > 1)
			.map((r) => {
				const top = r.map((i) => `L${x(i).toFixed(1)},${y(values[i] as number).toFixed(1)}`).join(' ');
				return `M${x(r[0]).toFixed(1)},${base} ${top} L${x(r[r.length - 1]).toFixed(1)},${base} Z`;
			})
			.join(' ');
	}

	const TICKS = [1, 0.75, 0.5, 0.25, 0];

	/** Up to six evenly spread labels along the bottom. */
	const axis = $derived.by(() => {
		if (n === 0) return [] as { label: string; x: number }[];
		const count = Math.min(6, n);
		const seen = new Set<number>();
		const out: { label: string; x: number }[] = [];
		for (let k = 0; k < count; k++) {
			const i = count === 1 ? 0 : Math.round((k * (n - 1)) / (count - 1));
			if (seen.has(i)) continue;
			seen.add(i);
			out.push({ label: bucketLabel(buckets[i], bucketSeconds), x: x(i) });
		}
		return out;
	});

	function onMove(e: PointerEvent) {
		if (!containerEl || n === 0) return;
		const rect = containerEl.getBoundingClientRect();
		const px = Math.max(PAD_L, Math.min(PAD_L + PW, e.clientX - rect.left));
		hoveredIdx = n < 2 ? 0 : Math.round(((px - PAD_L) / PW) * (n - 1));
	}

	const hovered = $derived.by(() => {
		if (hoveredIdx === null || hoveredIdx >= n) return null;
		const hx = x(hoveredIdx);
		return { i: hoveredIdx, x: hx, toLeft: hx > PAD_L + PW * 0.52 };
	});
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
	class="trend"
	bind:this={containerEl}
	bind:clientWidth={containerWidth}
	onpointermove={onMove}
	onpointerleave={() => (hoveredIdx = null)}
>
	<svg viewBox="0 0 {W} {height}" style="width: 100%; height: {height}px" role="img">
		<defs>
			{#each series.filter((s) => s.primary) as s (s.key)}
				<linearGradient id="trend-fill-{s.key}" x1="0" y1="0" x2="0" y2="1">
					<stop offset="0%" stop-color={s.color} stop-opacity="0.12" />
					<stop offset="100%" stop-color={s.color} stop-opacity="0" />
				</linearGradient>
			{/each}
		</defs>

		{#each TICKS as t (t)}
			{@const yPos = y(yMax * t)}
			<line x1={PAD_L} y1={yPos} x2={PAD_L + PW} y2={yPos} class="grid" class:base={t === 0} />
			<text x={W - 8} y={yPos + 3.5} class="y-label" text-anchor="end">{formatAxis(yMax * t)}</text>
		{/each}

		{#each series.filter((s) => s.primary) as s (s.key)}
			<path d={areaPath(s.values)} fill="url(#trend-fill-{s.key})" />
		{/each}

		{#each series as s (s.key)}
			<path d={linePath(s.values)} class="line" class:primary={s.primary} style="stroke: {s.color}" />
			{#each lonePoints(s.values) as i (i)}
				<circle cx={x(i)} cy={y(s.values[i] as number)} r="3" style="fill: {s.color}" />
			{/each}
		{/each}

		{#each axis as a (a.x)}
			<text x={a.x} y={height - 8} class="x-label" text-anchor="middle">{a.label}</text>
		{/each}

		{#if hovered}
			<line x1={hovered.x} y1={PAD_T} x2={hovered.x} y2={PAD_T + PH} class="slice-line" />
			{#each series as s (s.key)}
				{@const v = s.values[hovered.i]}
				{#if v !== null && v !== undefined}
					<circle cx={hovered.x} cy={y(v)} r={s.primary ? 4.5 : 3.5} class="slice-dot" style="fill: {s.color}" />
				{/if}
			{/each}
		{/if}
	</svg>

	{#if hovered}
		<div class="tooltip" class:to-left={hovered.toLeft} style="left: {(hovered.x / W) * 100}%">
			<div class="tt-head">{bucketTitle(buckets[hovered.i], bucketSeconds)}</div>
			{#each series as s (s.key)}
				{@const v = s.values[hovered.i]}
				<div class="tt-row" class:primary={s.primary}>
					<span class="tt-name"><i style="background: {s.color}"></i>{s.label}</span>
					<span class="tt-val">{v === null || v === undefined ? '—' : s.format(v)}</span>
				</div>
			{/each}
		</div>
	{/if}
</div>
{#if !hasData}<p class="trend-empty">{empty}</p>{/if}

<style>
	.trend {
		position: relative;
		width: 100%;
		user-select: none;
	}

	svg {
		display: block;
		cursor: crosshair;
	}

	.trend-empty {
		display: flex;
		align-items: center;
		justify-content: center;
		margin: 4px 0 0;
		font-size: 12px;
		color: var(--text-3);
	}

	.grid {
		stroke: oklch(0.24 0 0);
		stroke-width: 1;
		stroke-dasharray: 2 3;
	}

	.grid.base {
		stroke: oklch(0.3 0 0);
		stroke-dasharray: none;
	}

	.y-label,
	.x-label {
		font-family: var(--mono, 'IBM Plex Mono', monospace);
		font-size: 11px;
		font-variant-numeric: tabular-nums;
	}

	.y-label {
		fill: var(--text-2, oklch(0.72 0 0));
	}

	.x-label {
		fill: var(--text-3, oklch(0.55 0 0));
	}

	.line {
		fill: none;
		stroke-width: 2;
		stroke-linejoin: round;
		stroke-linecap: round;
	}

	.line.primary {
		stroke-width: 2.4;
	}

	.slice-line {
		stroke: oklch(0.92 0 0 / 0.55);
		stroke-width: 1.5;
		stroke-dasharray: 3 3;
		pointer-events: none;
	}

	.slice-dot {
		stroke: oklch(0.12 0 0);
		stroke-width: 2;
		pointer-events: none;
	}

	.tooltip {
		position: absolute;
		top: 50%;
		pointer-events: none;
		z-index: 30;
		background: oklch(0.16 0 0 / 0.96);
		backdrop-filter: blur(10px);
		border: 1px solid oklch(0.28 0 0);
		border-radius: 8px;
		padding: 10px 14px;
		box-shadow: 0 10px 28px rgba(0, 0, 0, 0.5);
		min-width: 200px;
		transform: translate(14px, -50%);
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.tooltip.to-left {
		transform: translate(calc(-100% - 14px), -50%);
	}

	.tt-head {
		font-size: 11px;
		font-weight: 600;
		color: var(--text, oklch(0.93 0 0));
		padding-bottom: 6px;
		margin-bottom: 2px;
		border-bottom: 1px solid oklch(0.24 0 0);
		font-family: var(--mono, monospace);
		white-space: nowrap;
	}

	.tt-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 14px;
		font-size: 11px;
		white-space: nowrap;
	}

	.tt-name {
		display: flex;
		align-items: center;
		gap: 7px;
		color: var(--text-2, oklch(0.68 0 0));
	}

	.tt-name i {
		width: 7px;
		height: 7px;
		border-radius: 2px;
		flex-shrink: 0;
	}

	.tt-val {
		font-family: var(--mono, monospace);
		font-variant-numeric: tabular-nums;
		font-weight: 500;
		color: var(--text, oklch(0.93 0 0));
	}

	.tt-row.primary .tt-val {
		font-weight: 600;
	}
</style>
