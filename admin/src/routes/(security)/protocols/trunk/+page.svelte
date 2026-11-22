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

	// The cascade as charts only, the product's on top: how long the voice
	// stalls, how much of that a person pays for in milliseconds, what speed and
	// what price in latency he gets, and who is on the cascade. The wires
	// themselves are below: what an agent reads when something is wrong.

	const WINDOWS: TrunkWindow[] = ['6h', '24h', '7d'];
	const REFRESH_MS = 60_000;

	type Scope = 'live' | 'stand';
	// People: the pool's own call of the two fates and the entry's own wires.
	// The stand: its process per wire and its call over hk-pipe.
	const SCOPE = {
		live: { route: 'copies2', voice: 'trunk-voice' },
		stand: { route: 'pipe-copies2', voice: 'pipe-voice' },
	};

	const ms = (v: number) => `${Math.round(v)} мс`;
	const count = (v: number) => Math.round(v).toLocaleString('ru');
	const pm = (v: number) => v.toFixed(1).replace('.', ',');
	const pct = (v: number) => `${v.toLocaleString('ru', { maximumFractionDigits: 2 })}%`;
	const mbps = (v: number) => `${Math.round(v)} Мбит/с`;

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
		if (key === 'copies1') return 'кадр один раз';
		if (key === 'copies2') return 'маршрут';
		if (key.startsWith('wire-')) return `стенд ${key.slice(5)}`;
		if (key === 'trunk-pool') return 'tcp';
		if (key === 'trunk-udp') return 'udp';
		if (key === 'trunk-h3dgram') return 'quic';
		if (key === 'trunk-voice') return 'набор голоса';
		if (key === 'pipe-voice') return 'стенд, набор голоса';
		if (key === 'pipe-copies2') return 'маршрут';
		if (key === 'pipe-copies1') return 'стенд, один раз';
		if (key.startsWith('trunk-')) return key.slice(6);
		if (key.startsWith('pipe-')) return `стенд ${key.slice(5)}`;
		return key;
	}

	function colorOf(key: string): string {
		const c = WIRE_COLOR[wireOf(key)] ?? '#7e7e7e';
		if (key === 'copies2' || key === 'pipe-copies2') return '#e8e8e8';
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
		/** the thresholds the readings are judged by, as dashed lines */
		limits?: { value: number; color: string }[];
		/** a count per bucket, drawn per hour of time */
		perHour?: boolean;
		/** a running total of what went over this many milliseconds */
		accumulate?: number;
		/** colour and label per group, for charts that split one thing by kind */
		colors?: Record<string, string>;
		labels?: Record<string, string>;
		/** which targets belong to this chart, over the section's own rule */
		allow?: (key: string) => boolean;
	}

	// 60 ms is three voice frames: what Discord hears as a stutter. 150 ms is a
	// word: what the roadmap counts a red minute by.
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
			title: 'сумма пауз сверх 150 мс',
			hint: 'Накопленным итогом до каждой точки: сколько миллисекунд звонок простоял в обрыве длиннее 150 мс за окно. Конец линии — это и есть простой человека в миллисекундах, а не в минутах.',
			format: ms,
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
	];

	const PEOPLE_CHARTS: ChartSpec[] = [
		{
			groups: ['users_hikkari', 'users_naive', 'users_reality'],
			title: 'людей на каскаде, по протоколу',
			hint: 'Сколько человек сидит через каскад и каким протоколом: установок в сети за каждую минуту отрезка.',
			format: count,
			colors: { users_hikkari: '#199e70', users_naive: '#3987e5', users_reality: '#9085e9' },
			labels: { users_hikkari: 'hikkari', users_naive: 'naive', users_reality: 'reality' },
		},
	];

	const HEARING_CHARTS: ChartSpec[] = [
		{
			groups: ['holes60_up', 'holes60_down'],
			title: 'дыр в звуке, % кадров',
			hint: 'Тот же звонок глазами приложения с буфером 60 мс: кадр, который не пришёл вовремя, — дыра. Вверх — слушатель у выхода, вниз — у входа. Цель меньше 1%.',
			format: pct,
			limits: [{ value: 1, color: '#e66767' }],
			colors: { holes60_up: '#3987e5', holes60_down: '#c98500' },
			labels: { holes60_up: 'вверх', holes60_down: 'вниз' },
		},
	];

	const WIRE_CHARTS: ChartSpec[] = [
		{
			groups: ['call_gap'],
			title: 'пауза звонка по проводам, мс',
			hint: 'Тот же звонок, прибитый к одному проводу: так было бы, если бы голос шёл только по нему. Маршрут выше это то, что выходит с двух проводов разом.',
			format: ms,
			limits: PAUSE_LIMITS,
		},
		{
			groups: ['call_rtt50'],
			title: 'задержка p50, мс',
			hint: 'Медиана круга звонка по проводу.',
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
			hint: 'Магистраль держит пул долгих соединений вход ↔ ядро. Член умер — поток переезжает на живой, кусок переотправляется, это пауза. Рост — на границе режут соединения.',
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
			limits: [{ value: 150, color: '#e66767' }],
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
	const routeKey = $derived(SCOPE[scope].route);
	const voiceKey = $derived(SCOPE[scope].voice);

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

	/** One line per target of the groups, per hour where the spec counts minutes. */
	function linesOf(chart: TrunkWireChart | null, c: ChartSpec, allow: (key: string) => boolean, bucketSeconds: number): TrendSeries[] {
		const scale = c.perHour ? 3600 / bucketSeconds : 1;
		const out: TrendSeries[] = [];
		for (const group of c.groups) {
			const lines = (chart?.lines ?? []).filter((l) => l.group === group && allow(l.key)).sort((x, y) => rank(x.key).localeCompare(rank(y.key)));
			for (const l of lines) {
				const values = l.values.map((v) => (v === null ? null : v * scale));
				out.push({
					key: c.groups.length > 1 ? `${group}|${l.key}` : l.key,
					label: c.labels?.[group] ?? labelOf(l.key),
					color: c.colors?.[group] ?? colorOf(l.key),
					values: c.accumulate === undefined ? values : running(values, c.accumulate),
					format: c.format,
				});
			}
		}
		return out;
	}

	const cards = (specs: ChartSpec[], chart: TrunkWireChart | null, allow: (key: string) => boolean) =>
		specs
			.map((c) => ({ ...c, lines: linesOf(chart, c, c.allow ?? allow, chart?.bucketSeconds ?? 3600) }))
			.filter((c) => c.lines.length > 0);

	const pauseCharts = $derived(cards(PAUSE_CHARTS, data, (k) => k === routeKey));
	const speedCharts = $derived(cards(SPEED_CHARTS, data, (k) => !isStand(k)));
	const peopleCharts = $derived(cards(PEOPLE_CHARTS, data, (k) => !isStand(k)));
	const hearingCharts = $derived(cards(HEARING_CHARTS, hearing, (k) => k === voiceKey));
	const wireCharts = $derived(cards(WIRE_CHARTS, data, (k) => isStand(k) === (scope === 'stand')));
	const poolCharts = $derived(cards(POOL_CHARTS, data, (k) => !isStand(k)));
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

		{#if (speedCharts.length && data) || (peopleCharts.length && data)}
			<h2 class="section">скорость, цена, люди</h2>
			<div class="grid">
				{#each speedCharts as c (c.groups.join())}
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
				{#each peopleCharts as c (c.groups.join())}
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

		{#if wireCharts.length && data}
			<h2 class="section">по проводам</h2>
			<div class="grid">
				{#each wireCharts as c (c.groups.join())}
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

		{#if hearingCharts.length && hearing}
			<h2 class="section">что слышит слушатель</h2>
			<div class="grid">
				{#each hearingCharts as c (c.groups.join())}
					<section class="card">
						<h2 title={c.hint}>{c.title}</h2>
						<TrendChart
							buckets={hearing.buckets}
							bucketSeconds={hearing.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							limits={c.limits}
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

		{#if pointCharts.length && points}
			<h2 class="section">точки перехода</h2>
			<div class="grid">
				{#each pointCharts as c (c.groups.join())}
					<section class="card">
						<h2 title={c.hint}>{c.groups.includes('point_up') ? `${c.title}, из ${members}` : c.title}</h2>
						<TrendChart
							buckets={points.buckets}
							bucketSeconds={points.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							limits={c.limits}
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
						<TrendChart
							buckets={egress.buckets}
							bucketSeconds={egress.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							limits={c.limits}
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

	.grid.one {
		grid-template-columns: 1fr;
		margin-top: 12px;
	}

	.card {
		min-width: 0;
		padding: 16px;
		background: oklch(0.2 0 0);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.muted {
		color: var(--text-2);
		font-size: 12px;
	}
</style>
