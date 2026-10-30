<script lang="ts">
	import { bucketLabel, bucketTitle, type ReachPoint } from '$lib/api/entry';

	// The doorway chart (roadmap 42 П9.9). Three stacked bands fill the plot
	// from 0% (bottom) to 100% (top) so the absolute scale is immediately clear.
	//
	// - Bottom (green): got through at once (0% -> firstPct)
	// - Middle (amber): needed retry / race on other relays (firstPct -> anyPct)
	// - Top (red): never got through (anyPct -> 100%)
	//
	// One point per bucket of the window. A bucket nobody attempted in has no
	// shares, and the bands break there: a run of measured buckets is one
	// shape, and a gap is a gap rather than a line drawn across it.

	interface Props {
		series: ReachPoint[];
		bucketSeconds: number;
		height?: number;
	}

	let { series, bucketSeconds, height = 160 }: Props = $props();

	let svgEl = $state<SVGSVGElement | null>(null);
	let hoveredIdx = $state<number | null>(null);

	const W = 900;
	const PAD_L = 8;
	const PAD_R = 42;
	const PAD_T = 12;
	const PAD_B = 14;

	const PW = W - PAD_L - PAD_R;
	const PH = $derived(height - PAD_T - PAD_B);

	// Scale is strictly 0% to 100%
	const y = (v: number) => PAD_T + (1 - Math.max(0, Math.min(100, v)) / 100) * PH;
	const n = $derived(series.length);
	const x = (i: number) => (n < 2 ? PAD_L + PW / 2 : PAD_L + (i / (n - 1)) * PW);

	type Pt = { x: number; firstPct: number; anyPct: number };

	/** Unbroken runs of measured buckets. A lone bucket is widened to a sliver so it shows. */
	const runs = $derived.by(() => {
		const out: Pt[][] = [];
		let run: Pt[] = [];
		series.forEach((d, i) => {
			if (d.anyPct === null || d.firstPct === null) {
				if (run.length) out.push(run);
				run = [];
				return;
			}
			run.push({ x: x(i), firstPct: d.firstPct, anyPct: d.anyPct });
		});
		if (run.length) out.push(run);
		const half = n < 2 ? PW / 2 : PW / (n - 1) / 3;
		return out.map((r) =>
			r.length > 1
				? r
				: [
						{ ...r[0], x: Math.max(PAD_L, r[0].x - half) },
						{ ...r[0], x: Math.min(PAD_L + PW, r[0].x + half) },
					],
		);
	});

	const measured = $derived(runs.length > 0);

	const line = (r: Pt[], key: 'firstPct' | 'anyPct') =>
		r.map((p, k) => `${k ? 'L' : 'M'}${p.x.toFixed(1)},${y(p[key]).toFixed(1)}`).join(' ');

	const firstLine = $derived(runs.map((r) => line(r, 'firstPct')).join(' '));
	const anyLine = $derived(runs.map((r) => line(r, 'anyPct')).join(' '));

	const bandFirst = $derived(
		runs
			.map((r) => {
				const y0 = y(0).toFixed(1);
				const pts = r.map((p) => `L${p.x.toFixed(1)},${y(p.firstPct).toFixed(1)}`).join(' ');
				return `M${r[0].x.toFixed(1)},${y0} ${pts} L${r[r.length - 1].x.toFixed(1)},${y0} Z`;
			})
			.join(' '),
	);

	const bandRetried = $derived(
		runs
			.map((r) => {
				const reverse = [...r]
					.reverse()
					.map((p) => `L${p.x.toFixed(1)},${y(p.anyPct).toFixed(1)}`)
					.join(' ');
				return `${line(r, 'firstPct')} ${reverse} Z`;
			})
			.join(' '),
	);

	const bandFailed = $derived(
		runs
			.map((r) => {
				const y100 = y(100).toFixed(1);
				const reverse = [...r]
					.reverse()
					.map((p) => `L${p.x.toFixed(1)},${y(p.anyPct).toFixed(1)}`)
					.join(' ');
				return `M${r[0].x.toFixed(1)},${y100} L${r[r.length - 1].x.toFixed(1)},${y100} ${reverse} Z`;
			})
			.join(' '),
	);

	const Y_TICKS = [100, 75, 50, 25, 0];

	const axis = $derived.by(() => {
		if (n === 0) return [] as { label: string; xPct: number }[];
		const count = Math.min(5, n);
		const idx = [
			...new Set(Array.from({ length: count }, (_, k) => (count === 1 ? 0 : Math.round((k * (n - 1)) / (count - 1))))),
		];
		return idx.map((i) => ({
			label: bucketLabel(series[i].bucket, bucketSeconds),
			xPct: n < 2 ? 50 : (i / (n - 1)) * 100,
		}));
	});

	function handlePointerMove(e: PointerEvent) {
		if (!svgEl || n === 0) return;
		const rect = svgEl.getBoundingClientRect();
		const svgX = ((e.clientX - rect.left) / rect.width) * W;
		const clampedX = Math.max(PAD_L, Math.min(PAD_L + PW, svgX));
		hoveredIdx = n < 2 ? 0 : Math.round(((clampedX - PAD_L) / PW) * (n - 1));
	}

	function handlePointerLeave() {
		hoveredIdx = null;
	}

	const hoveredPoint = $derived.by(() => {
		if (hoveredIdx === null || !series[hoveredIdx]) return null;
		const d = series[hoveredIdx];
		const px = x(hoveredIdx);
		if (d.anyPct === null || d.firstPct === null) {
			return { d, px, measured: false as const, isRightSide: px > PAD_L + PW * 0.52 };
		}
		return {
			d,
			px,
			measured: true as const,
			anyPct: d.anyPct,
			firstPct: d.firstPct,
			firstTry: d.firstPct,
			retried: Math.max(0, d.anyPct - d.firstPct),
			failed: Math.max(0, 100 - d.anyPct),
			isRightSide: px > PAD_L + PW * 0.52,
		};
	});
</script>

<div class="reach-chart-container">
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<svg
		bind:this={svgEl}
		viewBox="0 0 {W} {height}"
		preserveAspectRatio="none"
		style="height: {height}px"
		role="img"
		aria-label="доля людей, дошедших до сервиса, по дням (масштаб 0-100%)"
		onpointermove={handlePointerMove}
		onpointerleave={handlePointerLeave}
	>
		<defs>
			<linearGradient id="grad-first" x1="0" y1="0" x2="0" y2="1">
				<stop offset="0%" stop-color="oklch(0.72 0.15 155)" stop-opacity="0.45" />
				<stop offset="100%" stop-color="oklch(0.72 0.15 155)" stop-opacity="0.22" />
			</linearGradient>
			<linearGradient id="grad-retried" x1="0" y1="0" x2="0" y2="1">
				<stop offset="0%" stop-color="oklch(0.78 0.12 85)" stop-opacity="0.45" />
				<stop offset="100%" stop-color="oklch(0.78 0.12 85)" stop-opacity="0.25" />
			</linearGradient>
			<linearGradient id="grad-failed" x1="0" y1="0" x2="0" y2="1">
				<stop offset="0%" stop-color="oklch(0.65 0.20 25)" stop-opacity="0.60" />
				<stop offset="100%" stop-color="oklch(0.65 0.20 25)" stop-opacity="0.35" />
			</linearGradient>
		</defs>

		<!-- 100% - 0% Reference Grid lines -->
		{#each Y_TICKS as tick (tick)}
			{@const yPos = y(tick)}
			<line
				x1={PAD_L}
				y1={yPos}
				x2={PAD_L + PW}
				y2={yPos}
				class="grid"
				class:primary-tick={tick === 100 || tick === 0}
			/>
			<text x={W - 4} y={yPos + 3.5} class="y-label" text-anchor="end">{tick}%</text>
		{/each}

		<!-- Stacked Areas (Watertight stacked bands from top to bottom) -->
		<path d={bandFailed} fill="url(#grad-failed)" />
		<path d={bandRetried} fill="url(#grad-retried)" />
		<path d={bandFirst} fill="url(#grad-first)" />

		<!-- Boundary separator lines between bands -->
		<path d={anyLine} class="line-any" />
		<path d={firstLine} class="line-first" />

		<!-- Hover vertical slice and indicators -->
		{#if hoveredPoint?.measured}
			<line x1={hoveredPoint.px} y1={PAD_T} x2={hoveredPoint.px} y2={PAD_T + PH} class="slice-line" />
			<circle cx={hoveredPoint.px} cy={y(hoveredPoint.anyPct)} r="4.5" class="slice-dot dot-any" />
			<circle cx={hoveredPoint.px} cy={y(hoveredPoint.firstPct)} r="4.5" class="slice-dot dot-first" />
		{/if}
	</svg>

	<!-- Tooltip on hover -->
	{#if hoveredPoint?.measured}
		<div
			class="slice-tooltip"
			class:to-left={hoveredPoint.isRightSide}
			style="left: {(hoveredPoint.px / W) * 100}%; top: {Math.max(
				PAD_T + 10,
				Math.min(height - 40, y(hoveredPoint.firstPct)),
			)}px;"
		>
			<div class="tt-header">
				{bucketTitle(hoveredPoint.d.bucket, bucketSeconds)} · {hoveredPoint.d.attempts.toLocaleString('ru')} попыток
			</div>
			<div class="tt-rows">
				<div class="tt-row">
					<span class="tt-dot dot-first"></span>
					<span class="tt-name">С первой попытки</span>
					<span class="tt-val">{hoveredPoint.firstTry.toFixed(1)}%</span>
				</div>
				<div class="tt-row">
					<span class="tt-dot dot-retried"></span>
					<span class="tt-name">Со 2-го релея</span>
					<span class="tt-val">{hoveredPoint.retried.toFixed(1)}%</span>
				</div>
				<div class="tt-row">
					<span class="tt-dot dot-failed"></span>
					<span class="tt-name">Не дошли</span>
					<span class="tt-val bad">{hoveredPoint.failed.toFixed(1)}%</span>
				</div>
			</div>
		</div>
	{/if}

	<!-- X-Axis Date Labels -->
	<div class="axis" style="padding-left: {(PAD_L / W) * 100}%; padding-right: {(PAD_R / W) * 100}%">
		{#each axis as item (item.label + item.xPct)}
			<span class="axis-item" style="left: calc({(PAD_L / W) * 100}% + {item.xPct * (PW / W)}%)">{item.label}</span>
		{/each}
	</div>
</div>
{#if !measured}<p class="empty">за окно ни одной попытки — доли пока не измерены</p>{/if}

<style>
	.reach-chart-container {
		position: relative;
		width: 100%;
		user-select: none;
	}

	svg {
		display: block;
		width: 100%;
		cursor: crosshair;
	}

	.grid {
		stroke: oklch(0.25 0 0);
		stroke-width: 1;
		stroke-dasharray: 2 3;
	}

	.grid.primary-tick {
		stroke: oklch(0.3 0 0);
		stroke-dasharray: none;
	}

	.y-label {
		fill: var(--text-3, oklch(0.46 0 0));
		font-family: var(--mono, 'IBM Plex Mono', monospace);
		font-size: 10px;
		font-variant-numeric: tabular-nums;
		letter-spacing: -0.02em;
	}

	.line-any {
		fill: none;
		stroke: oklch(0.78 0.12 85 / 0.85);
		stroke-width: 1.4;
		stroke-dasharray: 4 3;
	}

	.line-first {
		fill: none;
		stroke: oklch(0.72 0.15 155);
		stroke-width: 2;
		stroke-linejoin: round;
		stroke-linecap: round;
	}

	/* Slice crosshair */
	.slice-line {
		stroke: oklch(0.92 0 0 / 0.7);
		stroke-width: 1.5;
		stroke-dasharray: 3 3;
		pointer-events: none;
	}

	.slice-dot {
		stroke: oklch(0.14 0 0);
		stroke-width: 2;
		pointer-events: none;
	}

	.dot-first {
		fill: oklch(0.72 0.15 155);
	}

	.dot-retried {
		fill: oklch(0.78 0.12 85);
	}

	.dot-any {
		fill: oklch(0.78 0.12 85);
	}

	.dot-failed {
		fill: oklch(0.65 0.2 25);
	}

	/* Floating slice tooltip */
	.slice-tooltip {
		position: absolute;
		pointer-events: none;
		z-index: 10;
		background: oklch(0.18 0 0 / 0.94);
		backdrop-filter: blur(8px);
		border: 1px solid oklch(0.32 0 0);
		border-radius: 7px;
		padding: 9px 12px;
		box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
		min-width: 175px;
		transform: translate(14px, -50%);
		transition: transform 60ms ease-out;
	}

	.slice-tooltip.to-left {
		transform: translate(calc(-100% - 14px), -50%);
	}

	.tt-header {
		font-size: 11px;
		font-weight: 600;
		color: var(--text, oklch(0.93 0 0));
		margin-bottom: 6px;
		padding-bottom: 5px;
		border-bottom: 1px solid oklch(0.26 0 0);
		font-family: var(--mono, monospace);
	}

	.tt-rows {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.tt-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		font-size: 11px;
	}

	.tt-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.tt-name {
		color: var(--text-2, oklch(0.65 0 0));
		margin-right: auto;
		white-space: nowrap;
	}

	.tt-val {
		font-family: var(--mono, monospace);
		font-variant-numeric: tabular-nums;
		font-weight: 600;
		color: var(--text, oklch(0.93 0 0));
	}

	.tt-val.bad {
		color: var(--bad, oklch(0.65 0.2 25));
	}

	/* Axis */
	.axis {
		position: relative;
		height: 18px;
		margin-top: 6px;
	}

	.axis-item {
		position: absolute;
		transform: translateX(-50%);
		font-family: var(--mono, 'IBM Plex Mono', monospace);
		font-size: 10px;
		color: var(--text-3, oklch(0.46 0 0));
		white-space: nowrap;
	}

	.empty {
		margin: 0;
		font-size: 12px;
		color: var(--text-3, oklch(0.42 0 0));
	}
</style>
