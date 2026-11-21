<script lang="ts">
	import type { PointRoute, PointSample } from '$lib/api/trunkPoints';

	let { routes, now }: { routes: PointRoute[]; now: number } = $props();

	const freshForMs = 5 * 60_000;
	const minute = new Intl.DateTimeFormat('ru', { hour: '2-digit', minute: '2-digit' });

	function state(sample: PointSample | null) {
		if (!sample || now - Date.parse(sample.at) > freshForMs) return { tone: 'unknown', label: 'Нет свежих данных' };
		if (sample.up === 0) return { tone: 'down', label: 'Молчит' };
		if (sample.silentSeconds > 0) return { tone: 'partial', label: 'Была тишина' };
		if (sample.up < sample.members) return { tone: 'partial', label: 'Часть соединений' };
		return { tone: 'up', label: 'Работает' };
	}
</script>

{#each routes as route (route.entry)}
	<section class="route" aria-label={`Маршрут ${route.entry} через точки к ${route.exit}`}>
		<div class="box endpoint">
			<span>Вход</span>
			<strong>{route.entry}</strong>
		</div>
		<div class="connector" aria-hidden="true"></div>
		<ul class="points" aria-label="Приёмники">
			{#each route.points as point (point.name)}
				{@const status = state(point.sample)}
				<li class="box point" data-tone={status.tone}>
					<div class="point-head">
						<strong>{point.name}</strong>
						<span class="status">{status.label}</span>
					</div>
					{#if point.sample}
						<div class="reading">
							<span>{point.sample.up}/{point.sample.members} соединений</span>
							<span>{point.sample.silentSeconds} с тишины</span>
							<time datetime={point.sample.at}>{minute.format(new Date(point.sample.at))}</time>
						</div>
					{/if}
				</li>
			{/each}
		</ul>
		<div class="connector" aria-hidden="true"></div>
		<div class="box endpoint">
			<span>Ядро и выход</span>
			<strong>{route.exit}</strong>
		</div>
	</section>
{/each}

<style>
	.route {
		display: grid;
		grid-template-columns: minmax(100px, 1fr) 36px minmax(220px, 2fr) 36px minmax(100px, 1fr);
		align-items: center;
		max-width: 880px;
		margin-bottom: 12px;
	}

	.box {
		padding: 12px 14px;
		background: oklch(0.2 0 0);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.endpoint {
		display: grid;
		gap: 3px;
	}

	.endpoint span,
	.reading {
		color: var(--text-2);
		font-size: 12px;
	}

	strong {
		font-size: 15px;
		overflow-wrap: anywhere;
	}

	.connector {
		position: relative;
		height: 1px;
		background: var(--border);
	}

	.connector::after {
		content: '';
		position: absolute;
		right: 5px;
		top: -3px;
		width: 6px;
		height: 6px;
		border: solid var(--text-2);
		border-width: 1px 1px 0 0;
		transform: rotate(45deg);
	}

	.points {
		display: grid;
		gap: 6px;
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.point {
		--status: var(--text-2);
		border-inline-start: 3px solid var(--status);
	}

	.point[data-tone='up'] {
		--status: #5bd6a5;
	}

	.point[data-tone='partial'] {
		--status: #e6b65c;
	}

	.point[data-tone='down'] {
		--status: #f08282;
	}

	.point-head,
	.reading {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		align-items: baseline;
		gap: 4px 8px;
	}

	.status {
		color: var(--status);
		font-size: 12px;
	}

	.reading {
		margin-top: 4px;
		font-variant-numeric: tabular-nums;
	}

	@media (max-width: 650px) {
		.route {
			grid-template-columns: minmax(0, 1fr);
		}

		.connector {
			width: 1px;
			height: 16px;
			margin-inline: auto;
		}

		.connector::after {
			inset: auto auto 4px -3px;
			transform: rotate(135deg);
		}
	}
</style>
