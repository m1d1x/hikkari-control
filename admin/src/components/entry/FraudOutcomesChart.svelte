<script lang="ts">
	interface ZoneSeries {
		name: string;
		color: string;
		fill: string;
		vals: number[];
	}

	interface Props {
		zones: ZoneSeries[];
		days: string[];
		height?: number;
	}

	let { zones, days, height = 150 }: Props = $props();

	const W = 900;
	const PAD = 10;

	let hoverIndex = $state<number | null>(null);
	let tipX = $state(0);
	let tipY = $state(0);
	let svgEl = $state<SVGSVGElement | null>(null);

	const n = $derived(Math.max(2, days.length));

	// Calculate cumulative values for stacking
	const cumulative = $derived.by(() => {
		const res: number[][] = [];
		for (let z = 0; z < zones.length; z++) {
			const arr: number[] = [];
			for (let i = 0; i < n; i++) {
				const prev = z > 0 ? res[z - 1][i] : 0;
				arr.push(prev + (zones[z].vals[i] ?? 0));
			}
			res.push(arr);
		}
		return res;
	});

	const maxVal = $derived.by(() => {
		if (cumulative.length === 0) return 100;
		const top = cumulative[cumulative.length - 1];
		return Math.max(10, ...top) * 1.1;
	});

	const x = (i: number) => (n <= 1 ? 0 : (i / (n - 1)) * W);
	const y = (v: number) => height - PAD - (v / maxVal) * (height - PAD * 2);

	function path(vals: number[]): string {
		if (!vals || vals.length === 0) return '';
		return vals.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(' ');
	}

	function onMouseMove(e: MouseEvent) {
		if (!svgEl || n <= 1) return;
		const rect = svgEl.getBoundingClientRect();
		const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
		hoverIndex = Math.min(n - 1, Math.max(0, Math.round(frac * (n - 1))));
		tipX = e.clientX + 14;
		tipY = e.clientY - 44;
	}

	function onMouseLeave() {
		hoverIndex = null;
	}
</script>

<div class="outcomes-chart-wrapper">
	<svg
		bind:this={svgEl}
		viewBox="0 0 {W} {height}"
		preserveAspectRatio="none"
		class="outcomes-svg"
		onmousemove={onMouseMove}
		onmouseleave={onMouseLeave}
		role="img"
		aria-label="Регистрации по зонам скоринга"
	>
		<!-- Horizontal grid lines -->
		{#each [0, 1, 2] as g}
			{@const gy = PAD + (g / 2) * (height - PAD * 2)}
			<line x1="0" y1={gy} x2={W} y2={gy} stroke="oklch(0.22 0 0)" stroke-width="1" />
		{/each}

		<!-- Draw largest cumulative first so bands stack cleanly -->
		{#each zones as _, i}
			{@const z = zones.length - 1 - i}
			{@const d = path(cumulative[z])}
			{#if d}
				<path d="{d} L{W},{height} L0,{height} Z" fill={zones[z].fill} />
				<path {d} fill="none" stroke={zones[z].color} stroke-width="1.3" stroke-linejoin="round" />
			{/if}
		{/each}

		<!-- Hover crosshair -->
		{#if hoverIndex !== null}
			{@const hx = x(hoverIndex)}
			<line x1={hx} y1="0" x2={hx} y2={height} stroke="oklch(0.5 0 0)" stroke-width="1" stroke-dasharray="2 2" />
		{/if}
	</svg>

	{#if days.length > 0}
		<div class="axis-x">
			<span>{days[0]}</span>
			{#if days.length > 4}
				<span>{days[Math.floor(days.length * 0.25)]}</span>
				<span>{days[Math.floor(days.length * 0.5)]}</span>
				<span>{days[Math.floor(days.length * 0.75)]}</span>
			{/if}
			<span>{days[days.length - 1]}</span>
		</div>
	{/if}

	{#if hoverIndex !== null && days[hoverIndex]}
		<div class="tooltip" style="left: {tipX}px; top: {tipY}px">
			<div class="tip-date">{days[hoverIndex]}</div>
			<div class="tip-items">
				{#each zones as z}
					{#if z.vals[hoverIndex] !== undefined}
						<span class="tip-item">
							<i class="swatch" style="background: {z.color}"></i>
							{z.name} <b>{Math.round(z.vals[hoverIndex])}</b>
						</span>
					{/if}
				{/each}
			</div>
		</div>
	{/if}
</div>

<style>
	.outcomes-chart-wrapper {
		position: relative;
		width: 100%;
	}

	.outcomes-svg {
		display: block;
		width: 100%;
		height: 150px;
		cursor: crosshair;
	}

	.axis-x {
		display: flex;
		justify-content: space-between;
		margin-top: 6px;
		font-family: var(--font-mono, monospace);
		font-size: 10px;
		color: oklch(0.42 0 0);
	}

	.tooltip {
		position: fixed;
		z-index: 50;
		pointer-events: none;
		padding: 6px 10px;
		border-radius: 6px;
		background: oklch(0.17 0 0);
		border: 1px solid oklch(0.24 0 0);
		font-family: var(--font-mono, monospace);
		font-size: 11px;
		color: oklch(0.93 0 0);
		white-space: nowrap;
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4);
	}

	.tip-date {
		font-size: 10px;
		color: oklch(0.62 0 0);
		margin-bottom: 3px;
	}

	.tip-items {
		display: flex;
		gap: 8px;
		align-items: center;
	}

	.tip-item {
		display: flex;
		align-items: center;
		gap: 4px;
	}

	.swatch {
		width: 6px;
		height: 6px;
		border-radius: 2px;
		display: inline-block;
	}
</style>
