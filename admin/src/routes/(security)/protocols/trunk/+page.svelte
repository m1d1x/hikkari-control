<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { onFreshAnswers } from '$lib/api/fresh';
	import PageShell from '$components/layout/PageShell.svelte';
	import TrendChart, { type TrendSeries } from '$components/entry/TrendChart.svelte';
	import StateRibbon from '$components/entry/StateRibbon.svelte';
	import TrunkRoutes from '$components/entry/TrunkRoutes.svelte';
	import { fetchTrunkWires, type TrunkWindow, type TrunkWireChart } from '$lib/api/trunkWires';
	import { fetchTrunkHearing } from '$lib/api/trunkHearing';
	import { fetchTrunkEgress } from '$lib/api/trunkEgress';
	import { fetchTrunkPoints, type TrunkPointChart } from '$lib/api/trunkPoints';
	import { USE_MOCK } from '$lib/config/mock';

	// The cascade as charts only. The route on one line, then: the voice (the
	// call and what the app would hear), the border points, the receivers'
	// egresses, the wires and the pool. Values that are judged by a threshold
	// carry it as a dashed line; nothing is explained in prose, the meaning of
	// a chart is in its heading's tooltip.

	const WINDOWS: TrunkWindow[] = ['6h', '24h', '7d'];
	const REFRESH_MS = 60_000;

	type Scope = 'live' | 'stand';
	// People: the pool's own call of the two fates and the entry's own wires.
	// The stand: its process per wire and its call over hk-pipe.
	const SCOPE = {
		live: { calls: ['copies1', 'copies2'], voice: 'trunk-voice' },
		stand: { calls: ['pipe-copies1', 'pipe-copies2'], voice: 'pipe-voice' },
	};

	const ms = (v: number) => `${Math.round(v)} мс`;
	const count = (v: number) => Math.round(v).toLocaleString('ru');
	const pm = (v: number) => v.toFixed(1).replace('.', ',');
	const pct = (v: number) => `${v.toLocaleString('ru', { maximumFractionDigits: 2 })}%`;

	const WIRE_COLOR: Record<string, string> = {
		pool: '#e66767',
		udp: '#199e70',
		h3dgram: '#2fa8b5',
		quic: '#3987e5',
		salamander: '#9085e9',
		awg: '#c98500',
		voice: '#e0e0e0',
	};

	function wireOf(key: string): string {
		const bare = key.replace(/^(wire|trunk|pipe)-/, '');
		return bare.startsWith('copies') ? 'pool' : bare;
	}

	function labelOf(key: string): string {
		if (key === 'copies1') return '1 копия';
		if (key === 'copies2') return '2 копии';
		if (key.startsWith('wire-')) return `стенд ${key.slice(5)}`;
		if (key === 'trunk-pool') return 'вход, пул';
		if (key === 'trunk-voice') return 'набор голоса';
		if (key === 'pipe-voice') return 'стенд, набор голоса';
		if (key.startsWith('trunk-')) return `вход ${key.slice(6)}`;
		if (key === 'pipe-copies1') return 'стенд, 1 копия';
		if (key === 'pipe-copies2') return 'стенд, 2 копии';
		if (key.startsWith('pipe-')) return `труба ${key.slice(5)}`;
		return key;
	}

	function colorOf(key: string): string {
		const c = WIRE_COLOR[wireOf(key)] ?? '#7e7e7e';
		return key === 'copies1' || key === 'pipe-copies1' ? '#c67ac1' : c;
	}

	// Fixed order: the pool first, then the entry's wires, then the stand.
	function rank(key: string): string {
		const head = key.startsWith('copies') || key.startsWith('pipe-copies') ? '0' : key.startsWith('trunk-') ? '1' : key.startsWith('pipe-') ? '2' : '3';
		return head + key;
	}

	const isStand = (key: string) => key.startsWith('pipe-') || key.startsWith('wire-') || key.endsWith(' (стенд)');

	interface ChartSpec {
		groups: string[];
		title: string;
		hint: string;
		format: (v: number) => string;
		/** the tolerance the readings are judged by, as a dashed line */
		limit?: number;
		/** a count per bucket, drawn per hour of time */
		perHour?: boolean;
		/** colour and label per group, for charts that split one thing by kind */
		colors?: Record<string, string>;
		labels?: Record<string, string>;
	}

	const VOICE_CHARTS: ChartSpec[] = [
		{
			groups: ['call_red'],
			title: 'красных минут на час',
			hint: 'Звонок идёт сам, без клиента: кадр каждые 20 мс до выхода и обратно. 1 копия — кадр ушёл один раз по выбранному проводу, так слышит канал самого Hikkari; 2 копии — тот же кадр по двум проводам разных судеб разом, так слышит обычный UDP клиента. Тут минуты с паузой больше 150 мс или потерей больше 1%, на час окна. Допуск решения 18.11 — одна такая минута на час.',
			format: pm,
			limit: 1,
			perHour: true,
		},
		{
			groups: ['call_gaps60'],
			title: 'пауз больше 60 мс на час',
			hint: 'Сколько раз звонок молчал дольше 60 мс, на час окна. Второй ранг голоса у селектора после красных минут.',
			format: pm,
			perHour: true,
		},
		{
			groups: ['holes60_up', 'holes60_down'],
			title: 'дыр в звуке, % кадров',
			hint: 'Тот же звонок глазами приложения с буфером 60 мс: кадр, который не пришёл вовремя, — дыра. Вверх — слушатель у выхода, вниз — у входа. Цель меньше 1%.',
			format: pct,
			limit: 1,
		},
	];

	const WIRE_CHARTS: ChartSpec[] = [
		{
			groups: ['call_red'],
			title: 'красных минут на час',
			hint: 'Минуты с паузой больше 150 мс или потерей больше 1%, по каждому проводу: по ним селектор выбирает, какой провод несёт голос.',
			format: pm,
			limit: 1,
			perHour: true,
		},
		{
			groups: ['call_rtt50'],
			title: 'задержка p50, мс',
			hint: 'Медиана круга звонка по проводу. При равных паузах голос берёт провод с меньшей задержкой.',
			format: ms,
		},
		{
			groups: ['call_rtt'],
			title: 'задержка p99, мс',
			hint: 'Хвост задержки звонка по проводу: что чувствует звонок в худшие секунды отрезка.',
			format: ms,
		},
		{
			groups: ['call_loss'],
			title: 'потерь на 1000',
			hint: 'Сколько датаграмм звонка из 1000 не вернулось, по проводу.',
			format: pm,
		},
	];

	const POOL_CHARTS: ChartSpec[] = [
		{
			groups: ['pool_dead_stalled', 'pool_dead_broken'],
			title: 'смертей членов пула в час',
			hint: 'Магистраль держит пул долгих соединений вход ↔ ядро. Член умер — поток переезжает на живой, кусок переотправляется, это пауза. Рост — на границе режут соединения. Не путать с выходами: там последний участок до интернета.',
			format: pm,
			perHour: true,
			colors: { pool_dead_stalled: '#c98500', pool_dead_broken: '#e66767' },
			labels: { pool_dead_stalled: 'замолчал', pool_dead_broken: 'оборвался' },
		},
	];

	const POINT_CHARTS: ChartSpec[] = [
		{
			groups: ['point_up'],
			title: 'соединений через точку',
			hint: 'Сколько соединений магистрали держала точка: упало в ноль — точка умерла, вход уводит сессию на другую.',
			format: count,
		},
		{
			groups: ['point_call_gap'],
			title: 'пауза звонка через точку, мс',
			hint: 'Худшая пауза пробного звонка за отрезок, по каждой точке перехода.',
			format: ms,
			limit: 150,
		},
	];

	const EGRESS_CHARTS: ChartSpec[] = [
		{
			groups: ['egress_rtt'],
			title: 'задержка через выход, мс',
			hint: 'Круг приёмник → выход через туннель. Растёт — выходу плохо, сторож готовится уйти на запасной.',
			format: ms,
		},
	];

	let data = $state<TrunkWireChart | null>(null);
	let hearing = $state<TrunkWireChart | null>(null);
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
			Promise.all([fetchTrunkWires(window), fetchTrunkHearing(window, 'trunk'), fetchTrunkEgress(window)]),
			fetchTrunkPoints(window),
		]);
		if (current !== request) return;
		if (detail.status === 'fulfilled') {
			[data, hearing, egress] = detail.value;
			loadError = null;
		} else {
			loadError = 'не удалось загрузить провода и выходы';
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
	const members = $derived(Math.max(1, ...(points?.routes ?? []).flatMap((r) => r.points.map((p) => p.sample?.members ?? 0))));

	const calls = $derived(SCOPE[scope].calls);
	const voiceKey = $derived(SCOPE[scope].voice);

	/** One line per target of the groups, per hour where the spec counts minutes. */
	function linesOf(chart: TrunkWireChart | null, c: ChartSpec, allow: (key: string) => boolean, bucketSeconds: number): TrendSeries[] {
		const scale = c.perHour ? 3600 / bucketSeconds : 1;
		const out: TrendSeries[] = [];
		for (const group of c.groups) {
			for (const l of chart?.lines ?? []) {
				if (l.group !== group || !allow(l.key)) continue;
				out.push({
					key: c.groups.length > 1 ? `${group}|${l.key}` : l.key,
					label: c.labels?.[group] ?? labelOf(l.key),
					color: c.colors?.[group] ?? colorOf(l.key),
					values: l.values.map((v) => (v === null ? null : v * scale)),
					format: c.format,
					primary: c.groups.length === 1 && l.key === 'copies2',
				});
			}
		}
		return out.sort((a, b) => a.label.localeCompare(b.label, 'ru'));
	}

	const cards = (specs: ChartSpec[], chart: TrunkWireChart | null, allow: (key: string) => boolean) =>
		specs
			.map((c) => ({ ...c, lines: linesOf(chart, c, allow, chart?.bucketSeconds ?? 3600) }))
			.filter((c) => c.lines.length > 0);

	const voiceCharts = $derived(cards(VOICE_CHARTS, data, (k) => calls.includes(k) || k === voiceKey));
	const wireCharts = $derived(cards(WIRE_CHARTS, data, (k) => isStand(k) === (scope === 'stand')));
	const poolCharts = $derived(cards(POOL_CHARTS, data, (k) => !isStand(k) === (scope !== 'stand')));
	const pointCharts = $derived(
		POINT_CHARTS.map((c) => ({
			...c,
			lines: (points?.lines ?? [])
				.filter((l) => c.groups.includes(l.group) && l.key.endsWith(' (стенд)') === (scope === 'stand'))
				.sort((a, b) => a.key.localeCompare(b.key))
				.map<TrendSeries>((l, i) => ({
					key: l.key,
					label: l.key.replace(' (стенд)', ''),
					color: ['#3987e5', '#c98500', '#9085e9', '#199e70', '#e66767'][i % 5],
					values: l.values,
					format: c.format,
				})),
		})).filter((c) => c.lines.length > 0),
	);

	const egressKeyOrder = $derived([...new Set((egress?.lines ?? []).map((l) => l.key))].sort());
	const carrierLines = $derived<TrendSeries[]>(
		egress?.lines
			.filter((l) => l.group === 'egress_carry')
			.sort((a, b) => a.key.localeCompare(b.key))
			.map((l) => ({
				key: l.key,
				label: l.key,
				color: ['#3987e5', '#c98500', '#9085e9', '#199e70', '#e66767'][egressKeyOrder.indexOf(l.key) % 5],
				values: l.values,
				format: count,
			})) ?? [],
	);
	const egressCharts = $derived(cards(EGRESS_CHARTS, egress, () => true));
</script>

<PageShell>
	<div class="page">
		<div class="head">
			<h1>провода магистрали</h1>
			<div class="chips">
				<a class="chip" href="/protocols/hikkari">hikkari изнутри</a>
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

		{#if voiceCharts.length && data}
			<h2 class="section">голос</h2>
			<div class="grid">
				{#each voiceCharts as c (c.groups.join())}
					<section class="card">
						<h2 title={c.hint}>{c.title}</h2>
						<div class="legend">
							{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
						</div>
						<TrendChart
							buckets={data.buckets}
							bucketSeconds={data.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							limit={c.limit}
							height={200}
						/>
					</section>
				{/each}
			</div>
		{/if}

		{#if pointCharts.length && points}
			<h2 class="section">точки перехода</h2>
			<div class="grid">
				{#each pointCharts as c (c.groups.join())}
					<section class="card">
						<h2 title={c.hint}>{c.groups.includes('point_up') ? `${c.title}, из ${members}` : c.title}</h2>
						<div class="legend">
							{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
						</div>
						<TrendChart
							buckets={points.buckets}
							bucketSeconds={points.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							limit={c.limit}
							height={190}
						/>
					</section>
				{/each}
			</div>
		{/if}

		{#if egress && (carrierLines.length || egressCharts.length)}
			<h2 class="section">выходы приёмников</h2>
			<div class="grid">
				<section class="card">
					<h2 title="Кто из выходов везёт приёмник в каждую минуту. Цвет сменился — сторож ушёл на запасной или hub сменил выход; момент смены это шов между двумя клетками.">кто везёт</h2>
					<StateRibbon buckets={egress.buckets} bucketSeconds={egress.bucketSeconds} series={carrierLines} />
				</section>
				{#each egressCharts as c (c.groups.join())}
					<section class="card">
						<h2 title={c.hint}>{c.title}</h2>
						<div class="legend">
							{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
						</div>
						<TrendChart
							buckets={egress.buckets}
							bucketSeconds={egress.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							limit={c.limit}
							height={190}
						/>
					</section>
				{/each}
			</div>
		{/if}

		{#if wireCharts.length && data}
			<h2 class="section">по проводам</h2>
			<div class="grid">
				{#each wireCharts as c (c.groups.join())}
					<section class="card">
						<h2 title={c.hint}>{c.title}</h2>
						<div class="legend">
							{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
						</div>
						<TrendChart
							buckets={data.buckets}
							bucketSeconds={data.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							limit={c.limit}
							height={190}
						/>
					</section>
				{/each}
			</div>
		{/if}

		{#if poolCharts.length && data}
			<h2 class="section">пул</h2>
			<div class="grid">
				{#each poolCharts as c (c.groups.join())}
					<section class="card">
						<h2 title={c.hint}>{c.title}</h2>
						<div class="legend">
							{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
						</div>
						<TrendChart
							buckets={data.buckets}
							bucketSeconds={data.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							limit={c.limit}
							height={190}
						/>
					</section>
				{/each}
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

	.card {
		min-width: 0;
		padding: 16px;
		background: oklch(0.2 0 0);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 12px;
		margin-bottom: 8px;
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

	.muted {
		color: var(--text-2);
		font-size: 12px;
	}
</style>
