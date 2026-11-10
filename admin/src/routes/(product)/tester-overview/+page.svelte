<script lang="ts">
	import WinChips from '$components/product/WinChips.svelte';
	import TesterList from '$components/product/TesterList.svelte';
	import TrendChart, { type TrendMark } from '$components/entry/TrendChart.svelte';
	import { fetchTesterCharts, type TesterLines } from '$lib/api/product';
	import type { TesterInstall } from '$lib/api/support';
	import { Windowed } from '$lib/product/windowed.svelte';

	// Each tester on their own: the minutes their tunnel was on and carried
	// nothing next to their own network to Russia (ya.ru, vk.com over the
	// direct path), so a broken tunnel reads apart from a broken home network.
	const m = new Windowed('24h', fetchTesterCharts);
	const d = $derived(m.data);
	let installs = $state<TesterInstall[]>([]);

	const C = {
		orange: 'oklch(0.78 0.16 65)',
		blue: 'oklch(0.70 0.16 230)',
		red: 'oklch(0.68 0.20 25)',
	};

	const minutes = (v: number) => `${Number.isInteger(v) || v >= 10 ? Math.round(v) : v.toFixed(1)} мин`;
	const ms = (v: number) => `${Math.round(v)} мс`;

	const sum = (vs: (number | null)[]) => {
		const seen = vs.filter((v): v is number => v !== null);
		return seen.length ? seen.reduce((a, v) => a + v, 0) : null;
	};
	const median = (vs: (number | null)[]) => {
		const seen = vs.filter((v): v is number => v !== null).sort((a, b) => a - b);
		if (!seen.length) return null;
		const mid = Math.floor(seen.length / 2);
		return seen.length % 2 ? seen[mid] : (seen[mid - 1] + seen[mid]) / 2;
	};
	const or = (v: number | null, f: (v: number) => string) => (v === null ? '-' : f(v));

	const EMPTY: TesterLines = { lost: [], ruMs: [], ruMissed: [] };
	const linesOf = (id: string): TesterLines => {
		const l = d?.testers[id];
		if (l) return l;
		const none = (d?.buckets ?? []).map(() => null);
		return d ? { lost: none, ruMs: none, ruMissed: none } : EMPTY;
	};

	/** Minutes nobody in Russia answered, as marks on the network chart. */
	const missedMarks = (l: TesterLines): TrendMark[] =>
		(d?.buckets ?? []).flatMap((at, i) => {
			const n = l.ruMissed[i];
			return n ? [{ at, label: `${Math.round(n)} мин без ответа`, color: C.red }] : [];
		});
</script>

<div class="head">
	<div class="head-info">
		<h1>Тестеры</h1>
	</div>
	<div class="head-controls">
		{#if m.loading && d}<span class="dim loading-text">обновление…</span>{/if}
		<WinChips bind:value={m.win} />
	</div>
</div>

{#if m.error}
	<div class="error-banner">Ошибка загрузки: {m.error}</div>
{/if}

{#if d}
	{#each installs as t (t.install_id)}
		{@const l = linesOf(t.install_id)}
		<section class="section-block">
			<div class="section-header">
				<h2 class="section-title">{t.alias}</h2>
			</div>
			<div class="pair">
				<div class="chart-card">
					<div class="card-header">
						<h3 class="card-title">Простой</h3>
						<span class="card-value mono">{or(sum(l.lost), minutes)}</span>
					</div>
					<TrendChart
						buckets={d.buckets}
						bucketSeconds={d.bucketSeconds}
						formatAxis={minutes}
						height={135}
						series={[{ key: 'lost', label: 'Простой', color: C.orange, primary: true, values: l.lost, format: minutes }]}
					/>
				</div>
				<div class="chart-card">
					<div class="card-header">
						<h3 class="card-title">Сеть до РФ</h3>
						<span class="card-value mono">{or(median(l.ruMs), ms)}</span>
					</div>
					<TrendChart
						buckets={d.buckets}
						bucketSeconds={d.bucketSeconds}
						formatAxis={ms}
						height={135}
						marks={missedMarks(l)}
						series={[{ key: 'ru', label: 'Сеть до РФ', color: C.blue, primary: true, values: l.ruMs, format: ms }]}
					/>
				</div>
			</div>
		</section>
	{/each}
{/if}

<TesterList bind:installs />

<style>
	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 16px;
		margin-bottom: 24px;
	}

	.head-info h1 {
		font-size: 22px;
		font-weight: 700;
		letter-spacing: -0.02em;
		color: var(--text);
		margin: 0;
	}

	.head-controls {
		display: flex;
		align-items: center;
		gap: 12px;
	}

	.loading-text {
		font-size: 12px;
	}

	.error-banner {
		padding: 12px 16px;
		margin-bottom: 20px;
		border-radius: 8px;
		background: rgba(220, 38, 38, 0.15);
		border: 1px solid rgba(220, 38, 38, 0.3);
		color: #f87171;
		font-size: 13px;
	}

	.section-block {
		margin-bottom: 24px;
	}

	.section-header {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		margin-bottom: 16px;
	}

	.section-title {
		font-size: 15px;
		font-weight: 600;
		color: var(--text);
		margin: 0;
	}

	.pair {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 16px;
	}

	.chart-card {
		background: var(--card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 16px;
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.card-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		margin-bottom: 8px;
	}

	.card-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
		margin: 0;
	}

	.card-value {
		font-size: 22px;
		font-weight: 600;
		color: var(--text);
		line-height: 1;
	}

	@media (max-width: 900px) {
		.pair {
			grid-template-columns: 1fr;
		}
	}
</style>
