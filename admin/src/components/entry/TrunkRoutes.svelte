<script lang="ts">
	import type { PointRoute, PointSample } from '$lib/api/trunkPoints';

	let { routes, now }: { routes: PointRoute[]; now: number } = $props();

	const minute = new Intl.DateTimeFormat('ru', { hour: '2-digit', minute: '2-digit' });

	function fresh(sample: PointSample | null): sample is PointSample {
		return sample !== null && now - Date.parse(sample.at) <= 5 * 60_000;
	}

	function reading(sample: PointSample | null): string {
		if (!sample) return 'нет замеров';
		const at = minute.format(new Date(sample.at));
		return `${sample.up} из ${sample.members} соединений, ${sample.silentSeconds} с тишины за минуту, замер в ${at}`;
	}
</script>

<ul class="routes entry-scope" aria-label="боевые маршруты">
	{#each routes as route (route.entry)}
		<li>
			<span class="m">{route.entry}</span>
			<span class="arrow" aria-hidden="true">→</span>
			{#each route.points as point (point.name)}
				{@const sample = point.sample}
				<span class="point" title={reading(sample)}>
					<span class="m">{point.name}</span>
					{#if fresh(sample)}
						<span
							class="m count"
							class:bad={sample.up === 0}
							class:warn={sample.up > 0 && (sample.up < sample.members || sample.silentSeconds > 0)}
						>
							{sample.up}/{sample.members}
						</span>
					{:else}
						<span class="count">нет свежих данных</span>
					{/if}
				</span>
			{/each}
			<span class="arrow" aria-hidden="true">→</span>
			<span class="m">{route.exit}</span>
		</li>
	{/each}
</ul>

<style>
	.routes {
		margin: 0;
		padding: 0;
		list-style: none;
	}
	li {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 3px 10px;
	}
	.point {
		white-space: nowrap;
	}
	.arrow,
	.count {
		color: var(--text-2);
	}
	.count {
		font-size: 12px;
	}
	.count.warn {
		color: var(--warn);
	}
	.count.bad {
		color: var(--bad);
	}
</style>
