<script lang="ts">
	/** One cell of the strip. */
	export interface StatCell {
		label: string;
		/** Already formatted; null draws "нет данных". */
		value: string | null;
		unit?: string;
		/** One short fact, not an explanation of what the label means. */
		meta?: string;
		tone?: 'ok' | 'warn' | 'bad';
		/** A capacity bar under the cell, 0..100. */
		barPct?: number | null;
	}

	/**
	 * Two to four figures that have no history worth drawing. Anything the hub
	 * keeps a series for belongs in a MetricHero instead — the strip is for
	 * facts that are true right now and move slowly, like a cap or a balance.
	 */
	interface Props {
		cells: StatCell[];
	}

	let { cells }: Props = $props();
</script>

<div class="strip">
	{#each cells as c (c.label)}
		<div class="cell">
			<span class="label">{c.label}</span>
			<div class="value {c.tone ?? ''}">
				{#if c.value === null}
					<span class="nodata">нет данных</span>
				{:else}
					{c.value}{#if c.unit}<span class="unit">{c.unit}</span>{/if}
				{/if}
			</div>
			{#if c.meta}<span class="meta">{c.meta}</span>{/if}
			{#if c.barPct !== null && c.barPct !== undefined}
				<div class="bar"><i style="width: {Math.min(100, Math.max(0, c.barPct))}%"></i></div>
			{/if}
		</div>
	{/each}
</div>

<style>
	.strip {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: 12px;
		margin-bottom: 16px;
	}

	.cell {
		background: var(--card-bg);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		padding: 13px 16px;
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}

	.label {
		font-size: 11px;
		color: var(--text-3);
	}

	.value {
		display: flex;
		align-items: baseline;
		gap: 5px;
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-size: 24px;
		font-weight: 600;
		letter-spacing: -0.02em;
		line-height: 1.1;
		color: var(--text);
	}

	.value.ok {
		color: var(--ok);
	}

	.value.warn {
		color: var(--warn);
	}

	.value.bad {
		color: var(--bad);
	}

	.unit {
		font-size: 12px;
		font-weight: 400;
		color: var(--text-3);
	}

	.nodata {
		font-family: var(--sans);
		font-size: 14px;
		font-weight: 500;
		color: var(--text-3);
		letter-spacing: 0;
	}

	.meta {
		font-size: 11px;
		color: var(--text-3);
	}

	.bar {
		margin-top: 6px;
		height: 3px;
		border-radius: 2px;
		background: var(--inset);
		overflow: hidden;
	}

	.bar i {
		display: block;
		height: 100%;
		background: var(--primary);
	}
</style>
