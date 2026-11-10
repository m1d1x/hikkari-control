<script lang="ts">
	// The product's main metric (ROADMAP 3.3), one line per person: a sum over
	// everyone would hide the one tunnel that carried nothing for an hour.
	import TrendChart from '$components/entry/TrendChart.svelte';
	import type { ProductDowntime } from '$lib/api/product';

	let { data }: { data: ProductDowntime } = $props();

	const COLORS = [
		'oklch(0.68 0.20 25)',
		'oklch(0.70 0.16 230)',
		'oklch(0.78 0.16 65)',
		'oklch(0.72 0.18 310)',
		'oklch(0.75 0.18 155)',
		'oklch(0.80 0.10 200)',
	];
	const color = (i: number) => COLORS[i % COLORS.length] ?? '#787878';
	const minutes = (v: number) => `${Math.round(v)} мин`;

	const series = $derived(
		data.people.map((p, i) => ({
			key: p.install,
			label: p.name,
			color: color(i),
			primary: i === 0 && p.lostTotal > 0,
			values: p.lost,
			format: minutes,
		})),
	);
</script>

<section class="downtime">
	<div class="head">
		<h2 class="title">Простой туннеля по людям</h2>
		<span class="sub">минуты, когда туннель включён, а трафик не идёт; без минут, когда лежала сеть самого человека</span>
	</div>
	<TrendChart
		buckets={data.buckets}
		bucketSeconds={data.bucketSeconds}
		formatAxis={minutes}
		height={190}
		empty="минуты придут с клиентов новее 1.0.78"
		{series}
	/>
	{#if data.people.length}
		<div class="legend">
			{#each data.people as p, i (p.install)}
				<span class="who"><i style="background:{color(i)}"></i>{p.name}</span>
			{/each}
		</div>
	{/if}
</section>

<style>
	.downtime {
		margin-bottom: 24px;
		padding: 16px;
		border: 1px solid var(--border);
		border-radius: 10px;
		background: var(--card);
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 4px 12px;
		margin-bottom: 8px;
	}
	.title {
		margin: 0;
		font-size: 15px;
		font-weight: 600;
	}
	.sub {
		color: var(--text-3);
		font-size: 12px;
	}
	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 6px 14px;
		margin-top: 6px;
		font-size: 12px;
	}
	.who {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	.who i {
		width: 8px;
		height: 8px;
		border-radius: 50%;
	}
</style>
