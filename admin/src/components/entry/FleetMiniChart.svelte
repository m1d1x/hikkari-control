<script lang="ts">
	interface LinesData {
		type: 'lines';
		series: { vals: number[]; color: string }[];
	}

	interface BarsData {
		type: 'bars';
		ok: number[];
		bad: number[];
	}

	interface AreaData {
		type: 'area';
		vals: number[];
		color: string;
	}

	type MiniData = LinesData | BarsData | AreaData;

	interface Props {
		data: MiniData;
		width?: number;
		height?: number;
	}

	let { data, width = 400, height = 48 }: Props = $props();

	const PAD = 6;

	function path(vals: number[], min: number, max: number): string {
		const n = vals.length;
		if (n === 0) return '';
		const x = (i: number) => (n < 2 ? 0 : (i / (n - 1)) * width);
		const y = (v: number) => height - PAD - ((v - min) / Math.max(1, max - min)) * (height - PAD * 2);
		return vals.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(' ');
	}
</script>

<svg class="mini" viewBox="0 0 {width} {height}" preserveAspectRatio="none">
	{#if data.type === 'lines'}
		{@const allVals = data.series.flatMap((s) => s.vals)}
		{@const min = Math.min(0, ...allVals) * 0.75}
		{@const max = Math.max(5, ...allVals) * 1.15}
		{#each data.series as s (s.color)}
			{@const d = path(s.vals, min, max)}
			{#if d}
				<path {d} fill="none" stroke={s.color} stroke-width="1.6" stroke-linejoin="round" />
			{/if}
		{/each}
	{:else if data.type === 'area'}
		{@const min = Math.min(0, ...data.vals) * 0.55}
		{@const max = Math.max(5, ...data.vals) * 1.15}
		{@const d = path(data.vals, min, max)}
		{#if d}
			<path d="{d} L{width},{height} L0,{height} Z" fill="oklch(0.72 0.15 155 / 0.12)" />
			<path {d} fill="none" stroke={data.color} stroke-width="1.6" stroke-linejoin="round" />
		{/if}
	{:else if data.type === 'bars'}
		{@const n = data.ok.length}
		{@const totals = data.ok.map((v, i) => v + data.bad[i])}
		{@const max = Math.max(5, ...totals) * 1.15}
		{@const slot = width / Math.max(1, n)}
		{@const bw = slot * 0.54}
		{#each Array.from({ length: n }) as _, i (i)}
			{@const x = slot * i + (slot - bw) / 2}
			{@const okH = (data.ok[i] / max) * (height - PAD * 2)}
			{@const okY = height - PAD - okH}
			<rect
				x={x.toFixed(1)}
				y={okY.toFixed(1)}
				width={bw.toFixed(1)}
				height={okH.toFixed(1)}
				fill="oklch(0.72 0.15 155 / 0.5)"
				rx="1"
			/>
			{#if data.bad[i] > 0}
				{@const badH = Math.max((data.bad[i] / max) * (height - PAD * 2), 2.5)}
				<rect
					x={x.toFixed(1)}
					y={(okY - badH).toFixed(1)}
					width={bw.toFixed(1)}
					height={badH.toFixed(1)}
					fill="var(--bad, oklch(0.65 0.2 25))"
					rx="1"
				/>
			{/if}
		{/each}
	{/if}
</svg>

<style>
	svg.mini {
		display: block;
		width: 100%;
		height: 46px;
		margin-top: 11px;
	}
</style>
