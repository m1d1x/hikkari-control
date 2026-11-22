<script lang="ts">
	import { bucketLabel, bucketTitle } from '$lib/api/entry';
	import type { TrendSeries } from './TrendChart.svelte';

	// A state over time, not a value: which egress carried the receiver, which
	// border point held the session. Coloured cells in a row, a seam is a
	// switch, the axis under it says when. Hovering a cell names the holder and
	// the span of its run: «unl1 → aeznl1, с 10:00 до 12:30».

	let {
		buckets,
		bucketSeconds,
		series,
		mode = 'carrier',
		empty = 'нет замеров за это окно',
	}: {
		buckets: string[];
		bucketSeconds: number;
		series: TrendSeries[];
		/** carrier: one row, the holder of the bucket; rows: one row per series, a live cell is a bucket it held */
		mode?: 'carrier' | 'rows';
		empty?: string;
	} = $props();

	const end = (iso: string) => new Date(Date.parse(iso) + bucketSeconds * 1000).toISOString();
	const span = $derived(bucketSeconds);

	const picked = $derived(
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
			return { at, held, label: best?.label ?? '', color: best?.color ?? null };
		}),
	);

	const bands = $derived(
		picked.map((b, i) => {
			let from = i;
			let to = i;
			if (b.label) {
				while (from > 0 && picked[from - 1].label === b.label) from--;
				while (to < picked.length - 1 && picked[to + 1].label === b.label) to++;
			}
			const title = b.label
				? `${b.label}, с ${bucketLabel(picked[from].at, span)} до ${bucketLabel(end(picked[to].at), span)}`
				: `${bucketTitle(b.at, span)} · никто не вёз`;
			return { ...b, title };
		}),
	);

	const ticks = $derived(
		[0, 0.25, 0.5, 0.75, 1].map((f) => {
			const i = Math.min(buckets.length - 1, Math.round(f * (buckets.length - 1)));
			return bucketLabel(buckets[i], span);
		}),
	);

	const hasData = $derived(
		mode === 'carrier' ? picked.some((b) => b.label !== '') : series.some((s) => s.values.some((v) => v !== null && v > 0)),
	);
</script>

{#if !hasData}
	<p class="empty">{empty}</p>
{:else}
	{#if mode === 'carrier'}
		<div class="ribbon" role="img" aria-label="кто вёз, по отрезкам">
			{#each bands as band (band.at)}
				<span class="cell" class:void={!band.label} style={band.color ? `background: ${band.color}` : ''} title={band.title}></span>
			{/each}
		</div>
	{:else}
		{#each series as s (s.key)}
			<div class="row">
				<span class="who">{s.label}</span>
				<div class="ribbon" role="img" aria-label={s.label}>
					{#each s.values as v, i (i)}
						<span
							class="cell"
							style={v ? 'background: oklch(0.27 0 0)' : 'background: #e66767'}
							title={`${s.label}, ${bucketTitle(buckets[i], span)} · ${v ? 'держит' : 'молчит'}`}
						></span>
					{/each}
				</div>
			</div>
		{/each}
	{/if}
	<div class="axis" class:offset={mode === 'rows'}>
		{#each ticks as t, i (i)}<span>{t}</span>{/each}
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

	.row {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.row + .row {
		margin-top: 3px;
	}

	.who {
		flex: 0 0 64px;
		font-size: 11px;
		color: var(--text-2);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.row .ribbon {
		flex: 1;
	}

	.axis.offset {
		padding-left: 72px;
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

	.empty {
		margin: 0;
		color: var(--text-2);
		font-size: 12px;
	}
</style>
