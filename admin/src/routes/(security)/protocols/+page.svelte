<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import TrendChart, { type TrendSeries } from '$components/entry/TrendChart.svelte';
	import { formatBytes } from '$lib/utils/format';
	import {
		fetchProtocolStats,
		describeEntryError,
		formatCents,
		PROTO_LABEL,
		type EntryProtocolStats,
		type OverviewWindow,
		type ProtocolSeries,
	} from '$lib/api/entry';

	// How each protocol and machine lived over the window. Everything here is a
	// line with history, on one time axis: the console draws data as history,
	// never as a number of the moment. A bucket nobody measured is a gap.

	const WINDOWS: OverviewWindow[] = ['24h', '7d', '30d'];
	const REFRESH_MS = 60_000;
	const ALL = 'all';

	// Eight hues in a fixed order, validated against this dark surface. A
	// machine keeps its colour across every chart, by its place among its
	// protocol's machines. A protocol keeps its by its name below: the first
	// three stay apart for every pair, which the console's badge colours do not
	// for a colour-blind reader.
	const NODE_COLORS = ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9', '#e66767'];
	const PROTO_COLOR: Record<string, string> = {
		'vless-reality-vision': NODE_COLORS[0],
		'vless-xhttp': NODE_COLORS[1],
		hysteria2: NODE_COLORS[2],
	};

	let data = $state<EntryProtocolStats | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<OverviewWindow>('24h');
	let group = $state<string>(ALL);
	let timer: ReturnType<typeof setInterval> | undefined;

	async function load() {
		try {
			data = await fetchProtocolStats(window);
			loadError = null;
		} catch (e) {
			loadError = describeEntryError(e);
		}
	}

	function pick(w: OverviewWindow) {
		if (w === window) return;
		window = w;
		void load();
	}

	onMount(() => {
		void load();
		timer = setInterval(load, REFRESH_MS);
	});
	onDestroy(() => clearInterval(timer));

	const inGroup = $derived((data?.nodes ?? []).filter((n) => n.proto === group));
	const shown = $derived(group === ALL ? (data?.protocols ?? []) : inGroup.slice(0, NODE_COLORS.length));
	const hidden = $derived(group === ALL ? 0 : Math.max(0, inGroup.length - NODE_COLORS.length));
	const bucketWord = $derived(
		data?.bucketSeconds === 3600 ? 'час' : data?.bucketSeconds === 21600 ? '6 часов' : 'сутки',
	);

	function colorOf(s: ProtocolSeries, i: number): string {
		return group === ALL ? (PROTO_COLOR[s.proto] ?? NODE_COLORS[3]) : NODE_COLORS[i];
	}

	function labelOf(s: ProtocolSeries): string {
		if (group === ALL) return PROTO_LABEL[s.proto] ?? s.proto;
		const parts = [s.key];
		if (s.domain) parts.push(s.domain);
		if (s.retired) parts.push('сдана');
		return parts.join(' · ');
	}

	const count = (v: number) => Math.round(v).toLocaleString('ru');
	const decimal = (v: number) => v.toLocaleString('ru', { maximumFractionDigits: 1 });
	const percent = (v: number) => `${v.toLocaleString('ru', { maximumFractionDigits: 1 })}%`;
	const money = (v: number) => formatCents(Math.round(v));

	type Metric = 'live' | 'passPct' | 'trafficBytes' | 'conns' | 'subscribers' | 'costCents';

	function lines(metric: Metric, format: (v: number) => string): TrendSeries[] {
		return shown.map((s, i) => ({
			key: s.key,
			label: labelOf(s),
			color: colorOf(s, i),
			values: s[metric],
			format,
		}));
	}

	/** An axis top for a whole-number figure: its four gridlines land on whole numbers. */
	function wholeTop(metric: Metric): number {
		const top = Math.max(0, ...shown.flatMap((s) => s[metric].map((v) => v ?? 0)));
		return Math.max(4, Math.ceil(top / 4) * 4);
	}

	const charts = $derived([
		{
			title: group === ALL ? 'Живые машины' : 'Живые релеи',
			metric: 'live' as Metric,
			format: count,
			max: wholeTop('live'),
		},
		{
			title: 'Проходимость из точки обзора',
			metric: 'passPct' as Metric,
			format: percent,
			max: 100,
			empty: 'пробник не судил за окно',
		},
		{ title: `Трафик за ${bucketWord}`, metric: 'trafficBytes' as Metric, format: formatBytes },
		{ title: `Подключения, среднее за ${bucketWord}`, metric: 'conns' as Metric, format: decimal },
		{ title: 'Подписчики', metric: 'subscribers' as Metric, format: count, max: wholeTop('subscribers') },
		{ title: 'Стоимость, в месяц', metric: 'costCents' as Metric, format: money },
	]);
</script>

<PageShell>
	<div class="protocols-page">
		<div class="head">
			<h1>протоколы</h1>
			<div class="chips">
				{#each WINDOWS as w (w)}
					<button class="chip" class:on={window === w} onclick={() => pick(w)}>{w}</button>
				{/each}
			</div>
		</div>

		{#if loadError}
			<p class="err">{loadError}</p>
		{/if}

		{#if !data && !loadError}
			<p class="hint">Загрузка…</p>
		{:else if data}
			<div class="filters">
				<div class="chips">
					<button class="chip" class:on={group === ALL} onclick={() => (group = ALL)}>все протоколы</button>
					{#each data.protocols as p (p.key)}
						<button class="chip" class:on={group === p.proto} onclick={() => (group = p.proto)}>
							{PROTO_LABEL[p.proto] ?? p.proto}
						</button>
					{/each}
				</div>
				<div class="legend">
					{#each shown as s, i (s.key)}
						<span class="legend-item"><i style="background: {colorOf(s, i)}"></i>{labelOf(s)}</span>
					{/each}
				</div>
			</div>

			<div class="grid">
				{#each charts as c (c.metric)}
					<section class="card">
						<h2>{c.title}</h2>
						<TrendChart
							buckets={data.buckets}
							bucketSeconds={data.bucketSeconds}
							series={lines(c.metric, c.format)}
							formatAxis={c.format}
							max={c.max}
							empty={c.empty}
							height={190}
						/>
					</section>
				{/each}
			</div>

			{#if hidden > 0}
				<p class="hint">Не показано машин: {hidden}. На графике не больше {NODE_COLORS.length} линий.</p>
			{/if}
			{#each data.missing as m (m.figure)}
				<p class="hint">Нет данных ({m.figure}): {m.why}</p>
			{/each}
		{/if}
	</div>
</PageShell>

<style>
	.protocols-page {
		color: var(--text, oklch(0.93 0 0));
		font-size: 13px;
		line-height: 1.5;
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		margin-bottom: 12px;
	}

	h1 {
		margin: 0;
		font-size: 18px;
		font-weight: 600;
	}

	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 8px 16px;
		margin-bottom: 12px;
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}

	.chip {
		min-height: 32px;
		padding: 4px 10px;
		border: 1px solid oklch(0.24 0 0);
		border-radius: 6px;
		background: none;
		color: oklch(0.62 0 0);
		font: inherit;
		font-size: 12px;
		cursor: pointer;
	}

	.chip.on {
		background: oklch(0.26 0 0);
		color: oklch(0.93 0 0);
	}

	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 14px;
		color: oklch(0.72 0 0);
		font-size: 12px;
	}

	.legend-item {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}

	.legend-item i {
		width: 12px;
		height: 3px;
		border-radius: 2px;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 400px), 1fr));
		gap: 12px;
		margin-bottom: 12px;
	}

	.card {
		min-width: 0;
		padding: 12px 14px 8px;
		background: oklch(0.2 0 0);
		border: 1px solid oklch(0.24 0 0);
		border-radius: 8px;
	}

	h2 {
		margin: 0 0 6px;
		font-size: 13px;
		font-weight: 600;
	}

	.err {
		margin: 0 0 12px;
		color: oklch(0.65 0.2 25);
	}

	.hint {
		margin: 0 0 8px;
		color: oklch(0.62 0 0);
	}
</style>
