<script lang="ts">
	import { bucketLabel, bucketTitle } from '$lib/api/entry';
	import type { TrendSeries } from './TrendChart.svelte';

	// A value that holds for a whole bucket and is one of a few: which egress
	// carried the receiver, which point held the session. Coloured minutes in a
	// row, so a switch reads as one colour replacing another, and its moment is
	// the seam between two cells.

	let {
		buckets,
		bucketSeconds,
		series,
		empty = 'нет замеров за это окно',
	}: {
		buckets: string[];
		bucketSeconds: number;
		/** one line per carrier; a bucket is the carrier with the most minutes in it */
		series: TrendSeries[];
		empty?: string;
	} = $props();

	const bands = $derived(
		buckets.map((at, i) => {
			let best: TrendSeries | null = null;
			let held = 0;
			for (const s of series) {
				const v = s.values[i];
				if (v !== null && v !== undefined && v > held) {
					held = v;
					best = s;
				}
			}
			const share = Math.min(1, held / (bucketSeconds / 60));
			return { at, held, share, had: best !== null, label: best?.label ?? '', color: best?.color ?? null };
		}),
	);

	const hasData = $derived(bands.some((b) => b.had));
</script>

{#if !hasData}
	<p class="empty">{empty}</p>
{:else}
	<div class="ribbon" role="img" aria-label="кто вёз, по отрезкам">
		{#each bands as band (band.at)}
			<span
				class="cell"
				class:void={!band.had}
				style={band.color ? `background: ${band.color}` : ''}
				title={`${bucketTitle(band.at, bucketSeconds)} · ${band.had ? `${band.label}, ${Math.round(band.held)} мин` : 'никто не вёз'}`}
			></span>
		{/each}
	</div>
	<div class="axis">
		<span>{bucketLabel(buckets[0], bucketSeconds)}</span>
		<span>{bucketLabel(buckets[buckets.length - 1], bucketSeconds)}</span>
	</div>
	<div class="legend">
		{#each series as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
	</div>
{/if}

<style>
	.ribbon {
		display: flex;
		width: 100%;
		height: 26px;
		border-radius: 3px;
		overflow: hidden;
	}

	.cell {
		flex: 1 1 0;
		min-width: 1px;
	}

	.cell.void {
		background: oklch(0.24 0 0);
	}

	.axis {
		display: flex;
		justify-content: space-between;
		margin-top: 4px;
		color: var(--text-2);
		font-family: var(--mono, 'IBM Plex Mono', monospace);
		font-size: 11px;
		font-variant-numeric: tabular-nums;
	}

	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 12px;
		margin-top: 8px;
		color: var(--text-2);
		font-size: 12px;
	}

	.legend i {
		display: inline-block;
		width: 8px;
		height: 8px;
		margin-right: 6px;
		border-radius: 2px;
	}

	.empty {
		margin: 0;
		color: var(--text-2);
		font-size: 12px;
	}
</style>
