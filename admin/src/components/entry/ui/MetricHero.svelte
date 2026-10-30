<script lang="ts">
	import type { Snippet } from 'svelte';

	/** One line of the breakdown under the headline figure. */
	export interface HeroRow {
		label: string;
		/** Already formatted: this component does not know the unit. */
		value: string;
		/** A colour swatch tying the line to its series on the chart. */
		color?: string;
		tone?: 'ok' | 'warn' | 'bad' | 'dim';
		/** Draws a divider above the line, for a derived total. */
		separated?: boolean;
	}

	/**
	 * The headline of a screen: one figure, what it is, how it moved, the
	 * lines it breaks into, and its history beside it.
	 *
	 * The chart is not optional by accident. A figure for the current moment
	 * alone only means something to someone who looked at it yesterday too;
	 * the reference screens pair every headline with its own time series, and
	 * a screen that has no series for a figure says "нет данных" instead of
	 * drawing the figure alone.
	 */
	interface Props {
		value: string | null;
		unit?: string;
		/** Movement against the window before this one, direction said. */
		trend?: { text: string; good: boolean } | null;
		/** What the figure is, in two or three words. Omitted when the
		 * breakdown under it already says what the lines are. */
		what?: string;
		rows?: HeroRow[];
		/** Shown in place of the figure when the hub cannot compute it. */
		missing?: string | null;
		chart: Snippet;
	}

	let { value, unit, trend, what, rows = [], missing = null, chart }: Props = $props();

	const caption = $derived(value === null ? (missing ?? what) : what);
</script>

<div class="hero">
	<div class="figure">
		<div class="big">
			{#if value === null}
				<span class="nodata">нет данных</span>
			{:else}
				{value}{#if unit}<span class="unit">{unit}</span>{/if}
				{#if trend}
					<span class="trend" class:good={trend.good} class:bad={!trend.good}>{trend.text}</span>
				{/if}
			{/if}
		</div>
		{#if caption}<div class="what">{caption}</div>{/if}
		{#if rows.length > 0}
			<div class="breakdown">
				{#each rows as r (r.label)}
					<div class:separated={r.separated}>
						<span class:dim={r.tone === 'dim'}>
							{#if r.color}<i class="sw" style="background: {r.color}"></i>{/if}
							{r.label}
						</span>
						<b class={r.tone ?? ''}>{r.value}</b>
					</div>
				{/each}
			</div>
		{/if}
	</div>
	<div class="chart">{@render chart()}</div>
</div>

<style>
	.hero {
		display: grid;
		grid-template-columns: 240px 1fr;
		gap: 24px;
		align-items: center;
	}

	.figure {
		display: flex;
		flex-direction: column;
		justify-content: center;
	}

	.big {
		display: flex;
		align-items: baseline;
		gap: 8px;
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-size: 42px;
		font-weight: 600;
		letter-spacing: -0.03em;
		line-height: 1;
		color: var(--text);
	}

	.big .unit {
		font-size: 16px;
		font-weight: 400;
		color: var(--text-3);
	}

	.nodata {
		font-family: var(--sans);
		font-size: 18px;
		font-weight: 500;
		color: var(--text-3);
		letter-spacing: 0;
	}

	.trend {
		font-family: var(--mono);
		font-size: 13px;
		font-weight: 500;
	}

	.trend.good {
		color: var(--ok);
	}

	.trend.bad {
		color: var(--bad);
	}

	.what {
		margin: 8px 0 0;
		font-size: 12px;
		color: var(--text-2);
	}

	.breakdown {
		margin-top: 14px;
		padding-top: 12px;
		border-top: 1px solid var(--border-subtle);
		display: flex;
		flex-direction: column;
		gap: 5px;
	}

	.breakdown div {
		display: flex;
		justify-content: space-between;
		gap: 10px;
		font-size: 11px;
		color: var(--text-3);
	}

	.breakdown div.separated {
		margin-top: 2px;
		padding-top: 6px;
		border-top: 1px dashed var(--border-subtle);
	}

	.breakdown div span {
		display: flex;
		align-items: center;
		gap: 7px;
	}

	.sw {
		width: 8px;
		height: 8px;
		border-radius: 2px;
		flex-shrink: 0;
	}

	.breakdown div b {
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-weight: 500;
		color: var(--text-2);
	}

	.breakdown div b.ok {
		color: var(--ok);
	}

	.breakdown div b.warn {
		color: var(--warn);
	}

	.breakdown div b.bad {
		color: var(--bad);
	}

	.chart {
		min-width: 0;
	}

	@media (max-width: 1100px) {
		.hero {
			grid-template-columns: 1fr;
		}
	}
</style>
