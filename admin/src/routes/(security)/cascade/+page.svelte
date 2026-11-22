<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { onFreshAnswers } from '$lib/api/fresh';
	import PageShell from '$components/layout/PageShell.svelte';
	import TrendChart, { type TrendSeries } from '$components/entry/TrendChart.svelte';
	import StateRibbon from '$components/entry/StateRibbon.svelte';
	import TrunkRoutes from '$components/entry/TrunkRoutes.svelte';
	import { fetchTrunkWires, type TrunkWindow, type TrunkWireChart } from '$lib/api/trunkWires';
	import { fetchTrunkEgress } from '$lib/api/trunkEgress';
	import { fetchTrunkPoints, type TrunkPointChart } from '$lib/api/trunkPoints';
	import { USE_MOCK } from '$lib/config/mock';

	// The cascade as the owner reads it: how long the voice stalls, what that
	// costs him in milliseconds, what speed and what latency a person gets, and
	// who carries the session. Seven charts, nothing else; the wires and the
	// pool live in the dataset an agent reads, not on this screen.

	const WINDOWS: TrunkWindow[] = ['6h', '24h', '7d', '30d'];
	const REFRESH_MS = 60_000;

	type Scope = 'live' | 'stand';
	const SCOPE = {
		live: { route: 'copies2', voice: 'trunk-voice' },
		stand: { route: 'pipe-copies2', voice: 'pipe-voice' },
	};

	const ms = (v: number) => `${Math.round(v)} мс`;
	const count = (v: number) => Math.round(v).toLocaleString('ru');
	const sec = (v: number) => `${(v / 1000).toLocaleString('ru', { maximumFractionDigits: v < 1000 ? 2 : 1 })} с`;
	const mbps = (v: number) => `${Math.round(v)} Мбит/с`;

	function labelOf(key: string): string {
		if (key === 'copies1') return 'кадр один раз';
		if (key === 'copies2') return 'маршрут';
		if (key === 'pipe-copies1') return 'стенд, один раз';
		if (key === 'pipe-copies2') return 'маршрут';
		if (key === 'trunk-udp') return 'udp';
		if (key === 'trunk-h3dgram') return 'quic';
		if (key === 'trunk-pool') return 'tcp';
		if (key === 'trunk-voice') return 'набор голоса';
		if (key.startsWith('pop') || key === 'aezamsk1') return key;
		return key.replace(/^(trunk|pipe|wire)-/, '');
	}

	function colorOf(key: string): string {
		if (key === 'copies2' || key === 'pipe-copies2') return '#e8e8e8';
		if (key === 'copies1' || key === 'pipe-copies1') return '#c67ac1';
		return '#9085e9';
	}

	const isStand = (key: string) => key.startsWith('pipe-') || key.startsWith('wire-') || key.endsWith(' (стенд)');

	interface ChartSpec {
		groups: string[];
		title: string;
		hint: string;
		format: (v: number) => string;
		limits?: { value: number; color: string }[];
		accumulate?: number;
		colors?: Record<string, string>;
		labels?: Record<string, string>;
		allow?: (key: string) => boolean;
	}

	// 60 ms is three voice frames: a stutter in a call. 150 ms is a word.
	const PAUSE_LIMITS = [
		{ value: 60, color: '#c98500' },
		{ value: 150, color: '#e66767' },
	];

	const PAUSE_CHARTS: ChartSpec[] = [
		{
			groups: ['call_gap'],
			title: 'пауза звонка, мс',
			hint: 'Худшая пауза пробного звонка за отрезок — как голос идёт на самом деле. Пунктир 60 мс: это уже заикание в звонке, 150 мс — проглоченное слово.',
			format: ms,
			limits: PAUSE_LIMITS,
		},
		{
			groups: ['call_gap'],
			title: 'сумма пауз сверх 150 мс, на человека',
			hint: 'Накопленным итогом: сколько миллисекунд один человек простоял в обрыве длиннее 150 мс за окно. Пробный звонок — это и есть один человек, поэтому сумма не по всему проекту, а на человека. Конец линии — цифра простоя в миллисекундах, а не в минутах.',
			format: sec,
			accumulate: 150,
		},
	];

	const SPEED_CHARTS: ChartSpec[] = [
		{
			groups: ['bulk_speed_p50', 'bulk_speed_p10'],
			title: 'скорость 1 МиБ, Мбит/с',
			hint: 'Что человек получает при загрузке через каскад: медиана и худшие 10% загрузок.',
			format: mbps,
			colors: { bulk_speed_p50: '#199e70', bulk_speed_p10: '#c98500' },
			labels: { bulk_speed_p50: 'медиана', bulk_speed_p10: 'худшие 10%' },
		},
		{
			groups: ['cascade_price'],
			title: 'цена каскада, мс',
			hint: 'Пинг через каскад минус пинг по России до того же адреса: сколько миллисекунд добавляет заграница. Это то, что человек чувствует в игре.',
			format: ms,
		},
		{
			groups: ['call_rtt50'],
			title: 'задержка p50, мс',
			hint: 'Медиана круга пробного звонка через каскад за отрезок.',
			format: ms,
		},
		{
			groups: ['call_rtt'],
			title: 'задержка p99, мс',
			hint: 'Хвост задержки: что чувствует звонок в худшие секунды отрезка.',
			format: ms,
		},
	];

	let data = $state<TrunkWireChart | null>(null);
	let egress = $state<TrunkWireChart | null>(null);
	let points = $state<TrunkPointChart | null>(null);
	let loadError = $state<string | null>(null);
	let pointsError = $state<string | null>(null);
	let window = $state<TrunkWindow>('24h');
	let scope = $state<Scope>('live');
	let now = $state(Date.now());
	let request = 0;
	let timer: ReturnType<typeof setInterval> | undefined;

	async function load() {
		const current = ++request;
		now = Date.now();
		const [detail, overview] = await Promise.allSettled([
			Promise.all([fetchTrunkWires(window), fetchTrunkEgress(window)]),
			fetchTrunkPoints(window),
		]);
		if (current !== request) return;
		if (detail.status === 'fulfilled') {
			[data, egress] = detail.value;
			loadError = null;
		} else {
			loadError = 'не удалось загрузить каскад';
		}
		if (overview.status === 'fulfilled') {
			points = overview.value;
			pointsError = null;
		} else {
			pointsError = 'не удалось обновить маршрут';
		}
	}

	function pick(w: TrunkWindow) {
		if (w === window) return;
		window = w;
		void load();
	}

	onFreshAnswers(load);
	onMount(() => {
		void load();
		timer = setInterval(load, REFRESH_MS);
	});
	onDestroy(() => clearInterval(timer));

	const routePoints = $derived(
		(points?.routes ?? [])
			.map((r) => ({ ...r, points: r.points.filter((p) => p.name.endsWith(' (стенд)') === (scope === 'stand')) }))
			.filter((r) => r.points.length > 0),
	);
	const hasStand = $derived((points?.lines ?? []).some((l) => isStand(l.key)));
	const entryName = $derived(routePoints[0]?.entry ?? '');
	const routeKey = $derived(SCOPE[scope].route);

	/** Running total of what went over the threshold: the product's milliseconds. */
	function running(values: (number | null)[], threshold: number): (number | null)[] {
		let sum = 0;
		let seen = false;
		return values.map((v) => {
			if (v === null) return seen ? sum : null;
			seen = true;
			if (v > threshold) sum += v - threshold;
			return sum;
		});
	}

	function linesOf(chart: TrunkWireChart | null, c: ChartSpec, allow: (key: string) => boolean): TrendSeries[] {
		const out: TrendSeries[] = [];
		for (const group of c.groups) {
			const lines = (chart?.lines ?? []).filter((l) => l.group === group && allow(l.key));
			for (const l of lines) {
				out.push({
					key: c.groups.length > 1 ? `${group}|${l.key}` : l.key,
					label: c.labels?.[group] ?? labelOf(l.key),
					color: c.colors?.[group] ?? colorOf(l.key),
					values: c.accumulate === undefined ? l.values : running(l.values, c.accumulate),
					format: c.format,
				});
			}
		}
		return out;
	}

	const cards = (specs: ChartSpec[], chart: TrunkWireChart | null, allow: (key: string) => boolean) =>
		specs
			.map((c) => ({ ...c, lines: linesOf(chart, c, c.allow ?? allow) }))
			.filter((c) => c.lines.length > 0);

	const pauseCharts = $derived(cards(PAUSE_CHARTS, data, (k) => k === routeKey));
	const restCharts = $derived(cards(SPEED_CHARTS, data, (k) => !isStand(k)));
	const pricedCharts = $derived(
		restCharts.map((c) => (c.groups.includes('cascade_price') && entryName ? { ...c, title: `цена каскада ${entryName}, мс` } : c)),
	);

	// The points: one row per point, a live cell is a bucket it held sessions in.
	const pointRows = $derived<TrendSeries[]>(
		(points?.lines ?? [])
			.filter((l) => l.group === 'point_up' && l.key.endsWith(' (стенд)') === (scope === 'stand'))
			.sort((a, b) => a.key.localeCompare(b.key))
			.map((l, i) => ({
				key: l.key,
				label: l.key.split(' → ').pop()?.replace(' (стенд)', '') ?? l.key,
				color: ['#3987e5', '#c98500', '#9085e9', '#199e70', '#e66767'][i % 5],
				values: l.values,
				format: count,
			})),
	);

	// The egresses: which one carried the receiver in each bucket. The receiver
	// that holds the tunnel is not the point people go through, so the strip
	// names the egress alone; two receivers behind one egress are one row.
	const carrierLines = $derived.by<TrendSeries[]>(() => {
		const byEgress = new Map<string, (number | null)[]>();
		for (const l of egress?.lines ?? []) {
			if (l.group !== 'egress_carry') continue;
			const name = l.key.split(' → ').pop() ?? l.key;
			const vals: (number | null)[] = l.values;
			const values: (number | null)[] = byEgress.get(name) ?? vals.map(() => null);
			vals.forEach((v, i) => {
				const held = values[i] ?? null;
				if (v !== null && (held === null || v > held)) values[i] = v;
			});
			byEgress.set(name, values);
		}
		return [...byEgress.entries()]
			.sort((a, b) => a[0].localeCompare(b[0]))
			.map(([name, values], i) => ({
				key: name,
				label: name,
				color: ['#3987e5', '#c98500', '#9085e9', '#199e70', '#e66767'][i % 5],
				values,
				format: count,
			}));
	});
</script>

<PageShell>
	<div class="page">
		<div class="head">
			<h1>каскад</h1>
			<div class="chips">
				{#each WINDOWS as w (w)}
					<button class="chip" class:on={window === w} aria-pressed={window === w} onclick={() => pick(w)}>{w}</button>
				{/each}
				{#if hasStand}
					<span class="sep" aria-hidden="true"></span>
					<button class="chip" class:on={scope === 'live'} aria-pressed={scope === 'live'} onclick={() => (scope = 'live')}
						>боевой</button
					>
					<button class="chip" class:on={scope === 'stand'} aria-pressed={scope === 'stand'} onclick={() => (scope = 'stand')}
						>стенд</button
					>
				{/if}
			</div>
		</div>

		{#if pointsError || loadError}
			<p class="err" role="status">{pointsError ?? loadError}</p>
		{/if}

		<div class="route">
			{#if routePoints.length}
				<TrunkRoutes routes={routePoints} {now} />
			{:else}
				<span class="muted">нет маршрутов через точки</span>
			{/if}
			{#if USE_MOCK}<span class="muted">демоданные</span>{/if}
		</div>

		{#if pauseCharts.length && data}
			<div class="grid one">
				{#each pauseCharts as c, i (c.title)}
					<section class="card">
						<h2 title={c.hint}>{c.title}</h2>
						<TrendChart
							buckets={data.buckets}
							bucketSeconds={data.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							limits={c.limits}
							height={i === 0 ? 260 : 190}
						/>
					</section>
				{/each}
			</div>
		{/if}

		{#if pricedCharts.length && data}
			<div class="grid">
				{#each pricedCharts as c (c.groups.join())}
					<section class="card">
						<h2 title={c.hint}>{c.title}</h2>
						<TrendChart
							buckets={data.buckets}
							bucketSeconds={data.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							limits={c.limits}
							height={190}
						/>
					</section>
				{/each}
			</div>
		{/if}

		{#if points && pointRows.length}
			<h2 class="section" title="Ровная полоса — точка держит соединения магистрали. Красный кусок — точка молчала, вход уводил сессию на другую.">точки перехода</h2>
			<div class="card">
				<StateRibbon buckets={points.buckets} bucketSeconds={points.bucketSeconds} series={pointRows} mode="rows" />
			</div>
		{/if}

		{#if egress && carrierLines.length}
			<h2 class="section">выходы приёмников</h2>
			<div class="card">
				<StateRibbon buckets={egress.buckets} bucketSeconds={egress.bucketSeconds} series={carrierLines} />
			</div>
		{/if}
	</div>
</PageShell>

<style>
	.page {
		padding: 20px 24px 40px;
	}

	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		margin-bottom: 8px;
	}

	h1 {
		margin: 0;
		font-size: 20px;
		font-weight: 600;
	}

	h2 {
		margin: 0 0 4px;
		font-size: 14px;
		font-weight: 600;
		cursor: help;
	}

	h2.section {
		margin: 24px 0 10px;
		font-size: 16px;
		cursor: default;
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 4px;
	}

	.chip {
		min-height: 32px;
		padding: 4px 10px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: none;
		color: var(--text-2);
		font: inherit;
		font-size: 12px;
		text-decoration: none;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
	}

	.chip.on {
		background: oklch(0.26 0 0);
		color: var(--text);
	}

	.sep {
		width: 1px;
		height: 18px;
		margin: 0 4px;
		background: var(--border);
	}

	.route {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 6px 16px;
		padding: 8px 0 4px;
		font-size: 13px;
	}

	.err {
		color: #e66767;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 420px), 1fr));
		gap: 12px;
	}

	.grid.one {
		grid-template-columns: 1fr;
		margin-top: 12px;
	}

	.grid + .grid {
		margin-top: 12px;
	}

	.card {
		min-width: 0;
		padding: 16px;
		background: oklch(0.2 0 0);
		border: 1px solid var(--border);
		border-radius: 8px;
		margin-bottom: 12px;
	}

	.grid .card {
		margin-bottom: 0;
	}

	.muted {
		color: var(--text-2);
		font-size: 12px;
		font-weight: 400;
	}
</style>
