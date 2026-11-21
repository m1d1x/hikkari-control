<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { onFreshAnswers } from '$lib/api/fresh';
	import PageShell from '$components/layout/PageShell.svelte';
	import TrendChart, { type TrendMark, type TrendSeries } from '$components/entry/TrendChart.svelte';
	import TrunkRoutes from '$components/entry/TrunkRoutes.svelte';
	import { fetchTrunkWires, type TrunkWindow, type TrunkWireChart } from '$lib/api/trunkWires';
	import { fetchTrunkHearing } from '$lib/api/trunkHearing';
	import { fetchTrunkEgress } from '$lib/api/trunkEgress';
	import { fetchTrunkPoints, type TrunkPointChart } from '$lib/api/trunkPoints';
	import { USE_MOCK } from '$lib/config/mock';

	// The trunk's wires through the border (stage 4): the entry's call over
	// each wire every 20 ms and its 1 MiB every 10 minutes, as history. The
	// pool's own call (copies1, copies2), the stand's process per wire
	// (wire-*), the entry's own wires (trunk-*) and the stand over hk-pipe
	// (pipe-*: its wires, and its calls of the route pipe-copies*) side by
	// side; the selector's moves of voice and bulk are marks on every chart.
	// Only charts: there is no number without its line.

	const WINDOWS: TrunkWindow[] = ['6h', '24h', '7d'];
	const REFRESH_MS = 60_000;
	const eventTime = new Intl.DateTimeFormat('ru', {
		month: 'short',
		day: 'numeric',
		hour: '2-digit',
		minute: '2-digit',
		second: '2-digit',
	});

	const ms = (v: number) => `${Math.round(v)} мс`;
	const count = (v: number) => Math.round(v).toLocaleString('ru');
	const pm = (v: number) => `${v.toFixed(1)} на 1000`;
	const sec = (v: number) => `${v.toFixed(1)} с`;
	const mbps = (v: number) => `${Math.round(v)} Мбит/с`;
	const cpu = (v: number) => `${v.toFixed(1)} с/мин`;

	// One colour per wire, whoever measured it: the stand's and the entry's
	// line of a wire differ by weight, not by hue.
	const WIRE_COLOR: Record<string, string> = {
		pool: '#e66767',
		quic: '#3987e5',
		salamander: '#9085e9',
		awg: '#c98500',
		udp: '#199e70',
		h3dgram: '#2fa8b5',
		voice: '#e0e0e0',
	};

	function wireOf(key: string): string {
		const bare = key.replace(/^(wire|trunk|pipe)-/, '');
		return bare.startsWith('copies') ? 'pool' : bare;
	}

	function labelOf(key: string): string {
		if (key === 'copies1') return 'пул, звонок 1 копией';
		if (key === 'copies2') return 'пул, звонок 2 копиями';
		if (key.startsWith('wire-')) return `стенд ${key.slice(5)}`;
		if (key === 'trunk-pool') return 'вход, пул';
		if (key === 'trunk-voice') return 'вход, набор голоса';
		if (key === 'pipe-voice') return 'труба, набор голоса';
		if (key.startsWith('trunk-')) return `вход ${key.slice(6)}`;
		if (key === 'pipe-copies1') return 'труба, звонок маршрута 1 копией';
		if (key === 'pipe-copies2') return 'труба, звонок маршрута 2 копиями';
		if (key.startsWith('pipe-')) return `труба ${key.slice(5)}`;
		return key;
	}

	function colorOf(key: string): string {
		const c = WIRE_COLOR[wireOf(key)] ?? '#7e7e7e';
		// The pool's call with one copy is the lighter of its two lines.
		return key === 'copies1' || key === 'pipe-copies1' ? '#d55181' : c;
	}

	// Lines in a fixed order: the pool first, then the entry's wires, then
	// the stand over hk-pipe, then the wire stand's.
	function rank(key: string): string {
		const head = key.startsWith('copies') ? '0' : key.startsWith('trunk-') ? '1' : key.startsWith('pipe-') ? '2' : '3';
		return head + key;
	}

	interface ChartSpec {
		group: string;
		title: string;
		hint: string;
		format: (v: number) => string;
	}
	const CHARTS: ChartSpec[] = [
		{
			group: 'call_red',
			title: 'Звонок: красные минуты',
			hint: 'минуты звонка магистрали с паузой больше 150 мс или потерей больше 1%, по проводу. Это ранг голоса у селектора.',
			format: count,
		},
		{
			group: 'call_gaps60',
			title: 'Звонок: паузы больше 60 мс',
			hint: 'сколько раз звонок молчал дольше 60 мс. Вместе с красными минутами решает, какой провод несёт голос.',
			format: count,
		},
		{
			group: 'call_gap',
			title: 'Звонок: самая длинная пауза',
			hint: 'худшая пауза звонка за отрезок. Волна входа на :x0 бьёт по всем проводам сразу, пул от неё стоит секунды.',
			format: ms,
		},
		{
			group: 'call_rtt',
			title: 'Звонок: задержка p99',
			hint: 'rtt p99 минут звонка, медиана за отрезок: при равных паузах голос берёт провод с меньшей.',
			format: ms,
		},
		{
			group: 'call_loss',
			title: 'Звонок: потери',
			hint: 'сколько датаграмм звонка из 1000 не вернулось.',
			format: pm,
		},
		{
			group: 'call_silent',
			title: 'Звонок: тишина',
			hint: 'сколько секунд за отрезок звонок не слышал ни одного ответа.',
			format: sec,
		},
		{
			group: 'bulk_mbps',
			title: '1 МиБ: скорость',
			hint: 'загрузка 1 МиБ по проводу раз в 10 минут, медиана дошедших целиком. Это ранг объёма у селектора. У стенда udp темп задан, а не измерен.',
			format: mbps,
		},
		{
			group: 'bulk_failed',
			title: '1 МиБ: не дошло целиком',
			hint: 'загрузки, которые оборвались или не уложились в срок. Провод с меньшим числом таких берёт объём раньше любого быстрого.',
			format: count,
		},
		{
			group: 'cpu',
			title: 'CPU входа на провод',
			hint: 'процессорное время процесса стенда на входе, секунд в минуту, медиана. У проводов в процессе входа по отдельности не считается.',
			format: cpu,
		},
	];

	// What a listener heard (hearing.go on the hub): a frame not there at its
	// moment of play with a 60 ms jitter buffer is a hole. Each set of wires
	// (набор голоса: the datagrams' wire and the pool) beside its wires.
	const pct = (v: number) => `${v.toLocaleString('ru', { maximumFractionDigits: 2 })}%`;
	const HEARING_CHARTS: ChartSpec[] = [
		{
			group: 'holes60_up',
			title: 'Слух вверх: дыры',
			hint: 'доля кадров звонка, которых слушатель у выхода не услышал бы при буфере 60 мс: не пришли или опоздали. Цель набора голоса меньше 1%.',
			format: pct,
		},
		{
			group: 'holes60_down',
			title: 'Слух вниз: дыры',
			hint: 'то же для слушателя у входа: ответы выхода, считая только кадры, которые выход получил.',
			format: pct,
		},
		{
			group: 'runs2_60_up',
			title: 'Слух вверх: серии',
			hint: 'серии из двух дыр и больше подряд: такое приложение не замажет, это проглоченный слог. Цель набора 0.',
			format: count,
		},
		{
			group: 'runs2_60_down',
			title: 'Слух вниз: серии',
			hint: 'серии из двух дыр и больше у слушателя у входа.',
			format: count,
		},
		{
			group: 'bad_s60_up',
			title: 'Слух вверх: плохие секунды',
			hint: 'секунды, где дыр 6% и больше (от 3 кадров из 50): это слышно как робоголос.',
			format: count,
		},
		{
			group: 'bad_s60_down',
			title: 'Слух вниз: плохие секунды',
			hint: 'плохие секунды у слушателя у входа.',
			format: count,
		},
	];

	// The receivers' egresses (H4): each receiver's own watch pings both of
	// its egresses through the tunnel once a second and moves to the spare
	// when the one that carries dies. Keys are "receiver → egress".
	const EGRESS_CHARTS: ChartSpec[] = [
		{
			group: 'egress_carry',
			title: 'Выход: кто везёт',
			hint: 'минуты отрезка, когда этот выход вёз приёмник. Смена цвета это уход сторожа или смена с hub.',
			format: count,
		},
		{
			group: 'egress_tun',
			title: 'Выход: туннель молчал',
			hint: 'секунды без ответа выхода через туннель (пинг раз в 200 мс). 1,5 с тишины у везущего, и сторож уходит на запасной.',
			format: count,
		},
		{
			group: 'egress_net',
			title: 'Выход: интернет молчал',
			hint: 'секунды, когда через везущий выход не ответили ни 1.1.1.1, ни 8.8.8.8. 5 с тишины при живом туннеле, и сторож уходит. У запасного интернет не меряется.',
			format: count,
		},
		{
			group: 'egress_rtt',
			title: 'Выход: задержка туннеля',
			hint: 'круг приёмник → выход через туннель, медиана минут.',
			format: ms,
		},
	];
	const EGRESS_COLORS = ['#3987e5', '#c98500', '#9085e9', '#199e70', '#e66767'];

	// The border points (G5): the entry holds one session to its core
	// through boxes that only forward the trunk's ports; member i of every
	// wire and pool goes through point i mod n. Keys are "entry → point".
	const POINT_CHARTS: ChartSpec[] = [
		{
			group: 'point_call_gap',
			title: 'Пауза звонка магистрали',
			hint: 'Максимум за отрезок, пробный звонок.',
			format: ms,
		},
		{
			group: 'point_silent',
			title: 'Точки перехода',
			hint: 'Время без живых соединений через точку.',
			format: sec,
		},
		{
			group: 'point_up',
			title: 'Точки перехода',
			hint: 'Минимум живых соединений за отрезок.',
			format: count,
		},
	];

	let data = $state<TrunkWireChart | null>(null);
	let hearing = $state<TrunkWireChart | null>(null);
	let egress = $state<TrunkWireChart | null>(null);
	let points = $state<TrunkPointChart | null>(null);
	let loadError = $state<string | null>(null);
	let pointsError = $state<string | null>(null);
	let window = $state<TrunkWindow>('24h');
	let pointScope = $state<'live' | 'stand'>('live');
	let pointMetric = $state<'point_silent' | 'point_up'>('point_silent');
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
			loadError = 'Не удалось загрузить графики проводов и выходов.';
		}
		if (overview.status === 'fulfilled') {
			points = overview.value;
			pointsError = null;
		} else {
			pointsError = 'Не удалось обновить обзор каскада.';
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

	const marks = $derived<TrendMark[]>(
		(data?.marks ?? []).map((m) => ({
			at: m.at,
			label: m.label,
			color: m.why === 'red' ? '#e66767' : m.class === 'voice' || m.class === 'voice2' ? '#199e70' : '#3987e5',
		})),
	);

	function linesOf(chart: TrunkWireChart | null, c: ChartSpec, primary: (key: string) => boolean): TrendSeries[] {
		return (chart?.lines ?? [])
			.filter((l) => l.group === c.group)
			.sort((a, b) => rank(a.key).localeCompare(rank(b.key)))
			.map<TrendSeries>((l) => ({
				key: l.key,
				label: labelOf(l.key),
				color: colorOf(l.key),
				values: l.values,
				format: c.format,
				primary: primary(l.key),
			}));
	}

	const charts = $derived(CHARTS.map((c) => ({ ...c, lines: linesOf(data, c, (k) => k.startsWith('trunk-')) })));
	const hearingCharts = $derived(
		HEARING_CHARTS.map((c) => ({ ...c, lines: linesOf(hearing, c, (k) => k.endsWith('-voice')) })),
	);

	const egressKeys = $derived([...new Set((egress?.lines ?? []).map((l) => l.key))].sort());
	const egressMarks = $derived<TrendMark[]>(
		(egress?.marks ?? []).map((m) => ({
			at: m.at,
			label: m.label,
			color: m.why === 'red' ? '#e66767' : m.why === 'stuck' ? '#c98500' : '#7e7e7e',
		})),
	);
	const egressCharts = $derived(
		EGRESS_CHARTS.map((c) => ({
			...c,
			lines: (egress?.lines ?? [])
				.filter((l) => l.group === c.group)
				.sort((a, b) => a.key.localeCompare(b.key))
				.map<TrendSeries>((l) => ({
					key: l.key,
					label: l.key,
					color: EGRESS_COLORS[egressKeys.indexOf(l.key) % EGRESS_COLORS.length],
					values: l.values,
					format: c.format,
					primary: true,
				})),
		})),
	);

	const pointLines = $derived(
		(points?.lines ?? []).filter((l) => l.key.endsWith(' (стенд)') === (pointScope === 'stand')),
	);
	const hasStand = $derived((points?.lines ?? []).some((l) => l.key.endsWith(' (стенд)')));
	const pointKeys = $derived([...new Set(pointLines.map((l) => l.key))].sort());
	const pointEvents = $derived(
		(points?.marks ?? []).filter((m) => m.class === (pointScope === 'stand' ? 'point-stand' : 'point')),
	);
	const pointMarks = $derived<TrendMark[]>(
		pointEvents.map((m) => ({ at: m.at, label: m.label, color: m.why === 'red' ? '#e66767' : '#199e70' })),
	);
	const pointCharts = $derived(
		POINT_CHARTS.filter((c) => c.group === pointMetric || (c.group === 'point_call_gap' && pointScope === 'live'))
			.map((c) => ({
				...c,
				lines: pointLines
					.filter((l) => l.group === c.group)
					.sort((a, b) => a.key.localeCompare(b.key))
					.map<TrendSeries>((l) => ({
						key: `${c.group}-${pointKeys.indexOf(l.key)}`,
						label:
							l.group !== 'point_call_gap' &&
							points?.routes?.length === 1 &&
							l.key.startsWith(points.routes[0].entry + ' → ')
								? l.key.split(' → ')[1].replace(' (стенд)', '')
								: l.key,
						color: EGRESS_COLORS[pointKeys.indexOf(l.key) % EGRESS_COLORS.length],
						values: l.values,
						format: c.format,
						primary: true,
					})),
			}))
			.filter((c) => c.lines.length > 0),
	);
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
			</div>
		</div>
		{#if pointsError}
			<p class="err" role="status">не удалось обновить точки перехода</p>
		{/if}
		{#if points}
			<div class="point-head">
				{#if points.routes?.length}
					<TrunkRoutes routes={points.routes} {now} />
				{:else}
					<span class="muted">нет маршрутов через точки</span>
				{/if}
				<div class="chips" role="group" aria-label="путь">
					{#if USE_MOCK}<span class="muted">демоданные</span>{/if}
					{#if hasStand || pointScope === 'stand'}
						<button
							class="chip"
							class:on={pointScope === 'live'}
							aria-pressed={pointScope === 'live'}
							onclick={() => (pointScope = 'live')}>боевой</button
						>
						<button
							class="chip"
							class:on={pointScope === 'stand'}
							aria-pressed={pointScope === 'stand'}
							onclick={() => (pointScope = 'stand')}>стенд</button
						>
					{/if}
				</div>
			</div>
			{#if points.window !== window && !pointsError}
				<p class="hint">загрузка истории…</p>
			{:else if points.window === window}
				{#if !pointCharts.length}
					<p class="hint">нет замеров за это окно</p>
				{/if}
				<div class="grid">
					{#each pointCharts as c (c.group)}
						<section class="card">
							<div class="chart-head">
								<h2>{c.title}</h2>
								{#if c.group !== 'point_call_gap'}
									<div class="chips" role="group" aria-label="график точек">
										<button
											class="chip"
											class:on={pointMetric === 'point_silent'}
											aria-pressed={pointMetric === 'point_silent'}
											onclick={() => (pointMetric = 'point_silent')}>тишина</button
										>
										<button
											class="chip"
											class:on={pointMetric === 'point_up'}
											aria-pressed={pointMetric === 'point_up'}
											onclick={() => (pointMetric = 'point_up')}>соединения</button
										>
									</div>
								{/if}
							</div>
							<p class="hint small">{c.hint}</p>
							<div class="legend">
								{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
							</div>
							<TrendChart
								buckets={points.buckets}
								bucketSeconds={points.bucketSeconds}
								series={c.lines}
								formatAxis={c.format}
								marks={pointMarks}
								height={190}
								empty="нет замеров за это окно"
							/>
						</section>
					{/each}
				</div>
				{#if pointEvents.length}
					<details class="point-events">
						<summary>события точек <span class="muted">{pointEvents.length}</span></summary>
						<ul class="events">
							{#each pointEvents.slice(-8).reverse() as event}
								<li class:down={event.why === 'red'}>
									<time datetime={event.at}>{eventTime.format(new Date(event.at))}</time>
									<span>{event.label}</span>
								</li>
							{/each}
						</ul>
					</details>
				{/if}
			{/if}
		{:else if !pointsError}
			<p class="hint">Загрузка каскада…</p>
		{/if}

		<h2 class="section">по проводам</h2>
		<p class="hint">
			Вход меряет каждый провод пробным звонком и загрузкой 1 МиБ. Отметки показывают, когда селектор перевёл голос или
			объём на другой провод.
		</p>
		{#if loadError}
			<p class="err">{loadError}</p>
		{/if}

		{#if !data && !loadError}
			<p class="hint">Загрузка…</p>
		{:else if data}
			<div class="grid">
				{#each charts as c (c.group)}
					<section class="card">
						<h2>{c.title}</h2>
						<p class="hint small">{c.hint}</p>
						<div class="legend">
							{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
						</div>
						<TrendChart
							buckets={data.buckets}
							bucketSeconds={data.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							{marks}
							height={190}
						/>
					</section>
				{/each}
			</div>
			{#if hearing}
				<h2 class="section">что слышит слушатель</h2>
				<p class="hint">
					Те же звонки глазами приложения с буфером 60 мс, по направлениям отдельно. Набор голоса: кадр услышан, если
					вовремя пришёл хоть по одному проводу набора (провод датаграмм и пул). Шаг минута до суток, неделя по 10
					минут.
				</p>
				<div class="grid">
					{#each hearingCharts as c (c.group)}
						<section class="card">
							<h2>{c.title}</h2>
							<p class="hint small">{c.hint}</p>
							<div class="legend">
								{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
							</div>
							<TrendChart
								buckets={hearing.buckets}
								bucketSeconds={hearing.bucketSeconds}
								series={c.lines}
								formatAxis={c.format}
								{marks}
								height={190}
								empty="слух не мерили за окно"
							/>
						</section>
					{/each}
				</div>
			{/if}
			{#if egress}
				<h2 class="section">выходы приёмников</h2>
				<p class="hint">
					Приёмник ходит в интернет через выход и держит запасной тёплым. Сторож на приёмнике раз в секунду пингует оба
					выхода через туннель раз в 200 мс и интернет через везущий; туннель везущего молчит 1,5 с (интернет за ним 5
					с), и сторож отдаёт маршруты запасному одним вызовом wg. Отметки: красная уход сторожа, жёлтая уйти некуда,
					серая смена с hub.
				</p>
				<div class="grid">
					{#each egressCharts as c (c.group)}
						<section class="card">
							<h2>{c.title}</h2>
							<p class="hint small">{c.hint}</p>
							<div class="legend">
								{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
							</div>
							<TrendChart
								buckets={egress.buckets}
								bucketSeconds={egress.bucketSeconds}
								series={c.lines}
								formatAxis={c.format}
								marks={egressMarks}
								height={190}
								empty="сторож выходов молчал за окно"
							/>
						</section>
					{/each}
				</div>
			{/if}
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
	}

	h2.section {
		margin: 24px 0 10px;
		font-size: 16px;
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

	.hint {
		margin: 0 0 12px;
		color: var(--text-2);
		font-size: 13px;
	}

	.hint.small {
		font-size: 12px;
		margin-bottom: 8px;
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

	.point-head,
	.chart-head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 4px 12px;
	}

	.point-head {
		margin-bottom: 12px;
	}

	.chart-head {
		min-height: 32px;
		margin-bottom: 4px;
	}

	.chart-head h2 {
		margin: 0;
	}

	.muted,
	.point-events summary {
		color: var(--text-2);
		font-size: 12px;
	}

	.point-events {
		margin: 8px 0 0;
	}

	.point-events summary {
		cursor: pointer;
		width: fit-content;
	}

	.events {
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.events li {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 14px;
		padding: 8px 0;
		border-bottom: 1px solid var(--border);
		font-size: 13px;
	}

	.events li:last-child {
		border: 0;
		padding-bottom: 0;
	}

	.events time {
		color: #199e70;
		font-variant-numeric: tabular-nums;
	}

	.events .down time {
		color: #e66767;
	}
</style>
