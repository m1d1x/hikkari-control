<script lang="ts">
	// The distribution of the scores the scorer actually gave, with the
	// thresholds in force drawn over it.
	//
	// Until К7 the bars were two Gaussians computed here — a shape that looked
	// like what a fraud histogram is supposed to look like, from a component
	// that had never seen an account. The bars are now the hub's own count per
	// 5-point slice of `fraud_scoring_log`, and an empty window draws no bars
	// at all rather than a plausible pile.
	interface ScoreBar {
		from: number;
		/** inclusive */
		to: number;
		count: number;
	}

	interface Props {
		bars: ScoreBar[];
		passMax: number;
		smsVerifyMax: number;
		shadowbanMax: number;
		height?: number;
	}

	let { bars, passMax = 30, smsVerifyMax = 60, shadowbanMax = 80, height = 150 }: Props = $props();

	const W = 900;
	const PAD = 8;

	const maxVal = $derived(Math.max(1, ...bars.map((b) => b.count)) * 1.12);
	const slot = $derived(W / Math.max(1, bars.length));
	const bw = $derived(slot * 0.82);
	const scored = $derived(bars.reduce((sum, b) => sum + b.count, 0));

	function zoneColor(score: number): string {
		if (score <= passMax) return 'var(--z-pass, oklch(0.72 0.15 155))';
		if (score <= smsVerifyMax) return 'var(--z-sms, oklch(0.78 0.12 85))';
		if (score <= shadowbanMax) return 'var(--z-shadow, oklch(0.7 0.17 45))';
		return 'var(--z-block, oklch(0.65 0.2 25))';
	}

	const bands = $derived([
		{ from: 0, to: passMax, fill: 'oklch(0.72 0.15 155 / 0.06)' },
		{ from: passMax, to: smsVerifyMax, fill: 'oklch(0.78 0.12 85 / 0.06)' },
		{ from: smsVerifyMax, to: shadowbanMax, fill: 'oklch(0.7 0.17 45 / 0.06)' },
		{ from: shadowbanMax, to: 100, fill: 'oklch(0.65 0.2 25 / 0.06)' },
	]);

	const segs = $derived([
		{ cls: 'zp', label: 'пропуск', range: `0–${passMax}`, width: passMax },
		{ cls: 'zs', label: 'смс', range: `${passMax + 1}–${smsVerifyMax}`, width: Math.max(5, smsVerifyMax - passMax) },
		{
			cls: 'zsh',
			label: 'теневой',
			range: `${smsVerifyMax + 1}–${shadowbanMax}`,
			width: Math.max(5, shadowbanMax - smsVerifyMax),
		},
		{ cls: 'zb', label: 'блок', range: `${shadowbanMax + 1}–100`, width: Math.max(5, 100 - shadowbanMax) },
	]);
</script>

<div class="hist-wrapper">
	<svg
		viewBox="0 0 {W} {height}"
		preserveAspectRatio="none"
		class="hist-svg"
		role="img"
		aria-label="Распределение очков по аккаунтам"
	>
		<!-- Faint zone background bands -->
		{#each bands as b}
			{@const bx = (b.from / 100) * W}
			{@const bwBand = ((b.to - b.from) / 100) * W}
			<rect x={bx} y="0" width={bwBand} {height} fill={b.fill} />
		{/each}

		<!-- Bars: the hub's counts, nothing smoothed -->
		{#each bars as bar, b}
			{@const bx = slot * b + (slot - bw) / 2}
			{@const bh = (bar.count / maxVal) * (height - PAD * 2)}
			{@const by = height - PAD - bh}
			{#if bar.count > 0}
				<rect x={bx} y={by} width={bw} height={bh} fill={zoneColor(bar.to)} rx="1">
					<title>{bar.from}–{bar.to} очков: {bar.count} оценок</title>
				</rect>
			{/if}
		{/each}

		<!-- Threshold dashed vertical lines -->
		{#each [passMax, smsVerifyMax, shadowbanMax] as thr}
			{@const tx = (thr / 100) * W}
			<line x1={tx} y1="0" x2={tx} y2={height} stroke="oklch(0.6 0 0)" stroke-width="1" stroke-dasharray="3 3" />
		{/each}
		{#if scored === 0}
			<text x={W / 2} y={height / 2} text-anchor="middle" fill="oklch(0.42 0 0)" font-size="13">
				за окно ни одной оценки — показывать нечего
			</text>
		{/if}
	</svg>

	<!-- Zone axis underneath -->
	<div class="zone-axis">
		{#each segs as s}
			<span class="zone-seg {s.cls}" style="flex: {s.width}">
				<b>{s.label}</b>
				{s.range}
			</span>
		{/each}
	</div>
</div>

<style>
	.hist-wrapper {
		width: 100%;
	}

	.hist-svg {
		display: block;
		width: 100%;
		height: 150px;
	}

	.zone-axis {
		display: flex;
		margin-top: 6px;
		gap: 2px;
	}

	.zone-seg {
		padding: 4px 8px;
		font-family: var(--font-mono, monospace);
		font-size: 10px;
		color: oklch(0.62 0 0);
		border-radius: 3px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.zone-seg b {
		font-weight: 600;
	}

	.zone-seg.zp {
		background: oklch(0.72 0.15 155 / 0.1);
		color: var(--z-pass, oklch(0.72 0.15 155));
	}

	.zone-seg.zs {
		background: oklch(0.78 0.12 85 / 0.1);
		color: var(--z-sms, oklch(0.78 0.12 85));
	}

	.zone-seg.zsh {
		background: oklch(0.7 0.17 45 / 0.1);
		color: var(--z-shadow, oklch(0.7 0.17 45));
	}

	.zone-seg.zb {
		background: oklch(0.65 0.2 25 / 0.1);
		color: var(--z-block, oklch(0.65 0.2 25));
	}
</style>
