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

	// The cascade on one screen. Top to bottom: the route as one line, four
	// numbers that answer "does it work", two charts of the people's voice,
	// and then everything else behind a click. A number is shown only when it
	// was measured; what a metric means lives in the heading's tooltip.

	const WINDOWS: TrunkWindow[] = ['6h', '24h', '7d'];
	const REFRESH_MS = 60_000;
	const OK = '#199e70';
	const WARN = '#c98500';
	const BAD = '#e66767';

	type Scope = 'live' | 'stand';
	// People: the pool's own call of the two fates and the entry's own wires.
	// The stand: its process per wire and its call over hk-pipe.
	const SCOPE = {
		live: { calls: ['copies1', 'copies2'], voice: 'trunk-voice' },
		stand: { calls: ['pipe-copies1', 'pipe-copies2'], voice: 'pipe-voice' },
	};
	const primary = (k: string) => k.startsWith('trunk-') || k.startsWith('pipe-copies');

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
	const pct = (v: number) => `${v.toLocaleString('ru', { maximumFractionDigits: 2 })}%`;

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
		if (key === 'copies1') return 'пул, 1 копия';
		if (key === 'copies2') return 'пул, 2 копии';
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
		// The pool's call with one copy is the lighter of its two lines.
		return key === 'copies1' || key === 'pipe-copies1' ? '#c67ac1' : c;
	}

	// Lines in a fixed order: the pool first, then the entry's wires, then
	// the stand over hk-pipe, then the wire stand's.
	function rank(key: string): string {
		const head = key.startsWith('copies') || key.startsWith('pipe-copies') ? '0' : key.startsWith('trunk-') ? '1' : key.startsWith('pipe-') ? '2' : '3';
		return head + key;
	}

	const isStand = (key: string) => key.startsWith('pipe-') || key.startsWith('wire-') || key.endsWith(' (стенд)');

	interface ChartSpec {
		group: string;
		title: string;
		hint: string;
		format: (v: number) => string;
	}

	const WIRE_CHARTS: ChartSpec[] = [
		{
			group: 'call_gaps60',
			title: 'Звонок: паузы больше 60 мс',
			hint: 'Сколько раз звонок молчал дольше 60 мс. Вместе с красными минутами решает, какой провод несёт голос.',
			format: count,
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
			hint: 'Сколько датаграмм звонка из 1000 не вернулось.',
			format: pm,
		},
		{
			group: 'call_silent',
			title: 'Звонок: тишина',
			hint: 'Сколько секунд за отрезок звонок не слышал ни одного ответа.',
			format: sec,
		},
		{
			group: 'bulk_mbps',
			title: '1 МиБ: скорость',
			hint: 'Загрузка 1 МиБ по проводу раз в 10 минут, медиана дошедших целиком. Это ранг объёма у селектора. У стенда udp темп задан, а не измерен.',
			format: mbps,
		},
		{
			group: 'bulk_failed',
			title: '1 МиБ: не дошло целиком',
			hint: 'Загрузки, которые оборвались или не уложились в срок. Провод с меньшим числом таких берёт объём раньше любого быстрого.',
			format: count,
		},
		{
			group: 'cpu',
			title: 'CPU входа на провод',
			hint: 'Процессорное время процесса стенда на входе, секунд в минуту, медиана. У проводов в процессе входа по отдельности не считается.',
			format: cpu,
		},
	];

	const HEARING_CHARTS: ChartSpec[] = [
		{
			group: 'holes60_up',
			title: 'Слух вверх: дыры',
			hint: 'Доля кадров звонка, которых слушатель у выхода не услышал бы при буфере 60 мс: не пришли или опоздали. Цель набора голоса меньше 1%.',
			format: pct,
		},
		{
			group: 'holes60_down',
			title: 'Слух вниз: дыры',
			hint: 'То же для слушателя у входа: ответы выхода, считая только кадры, которые выход получил.',
			format: pct,
		},
		{
			group: 'runs2_60_up',
			title: 'Слух вверх: серии',
			hint: 'Серии из двух дыр и больше подряд: такое приложение не замажет, это проглоченный слог. Цель набора 0.',
			format: count,
		},
		{
			group: 'runs2_60_down',
			title: 'Слух вниз: серии',
			hint: 'Серии из двух дыр и больше у слушателя у входа.',
			format: count,
		},
		{
			group: 'bad_s60_up',
			title: 'Слух вверх: плохие секунды',
			hint: 'Секунды, где дыр 6% и больше (от 3 кадров из 50): это слышно как робоголос.',
			format: count,
		},
		{
			group: 'bad_s60_down',
			title: 'Слух вниз: плохие секунды',
			hint: 'Плохие секунды у слушателя у входа.',
			format: count,
		},
	];

	const EGRESS_CHARTS: ChartSpec[] = [
		{
			group: 'egress_carry',
			title: 'Выход: кто везёт',
			hint: 'Минуты отрезка, когда этот выход вёз приёмник. Смена цвета это уход сторожа или смена с hub.',
			format: count,
		},
		{
			group: 'egress_tun',
			title: 'Выход: туннель молчал',
			hint: 'Секунды без ответа выхода через туннель (пинг раз в 200 мс). 1,5 с тишины у везущего, и сторож уходит на запасной.',
			format: count,
		},
		{
			group: 'egress_net',
			title: 'Выход: интернет молчал',
			hint: 'Секунды, когда через везущий выход не ответили ни 1.1.1.1, ни 8.8.8.8. 5 с тишины при живом туннеле, и сторож уходит. У запасного интернет не меряется.',
			format: count,
		},
		{
			group: 'egress_rtt',
			title: 'Выход: задержка туннеля',
			hint: 'Круг приёмник → выход через туннель, медиана минут.',
			format: ms,
		},
	];
	const EGRESS_COLORS = ['#3987e5', '#c98500', '#9085e9', '#199e70', '#e66767'];

	const POINT_CHARTS: ChartSpec[] = [
		{
			group: 'point_call_gap',
			title: 'Пауза звонка через точку',
			hint: 'Максимум паузы звонка магистрали за отрезок, по каждой точке перехода. Точка только пересылает порты на ядро выхода.',
			format: ms,
		},
		{
			group: 'point_silent',
			title: 'Точка: тишина',
			hint: 'Секунды отрезка, когда через точку не шло ни одно соединение магистрали.',
			format: sec,
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
			.map((r) => ({
				...r,
				points: r.points.filter((p) => p.name.endsWith(' (стенд)') === (scope === 'stand')),
			}))
			.filter((r) => r.points.length > 0),
	);
	const hasStand = $derived((points?.lines ?? []).some((l) => isStand(l.key)));

	const marks = $derived<TrendMark[]>(
		(data?.marks ?? [])
			.filter((m) => m.label.startsWith('труба') === (scope === 'stand'))
			.map((m) => ({
				at: m.at,
				label: m.label,
				color: m.why === 'red' ? BAD : m.class === 'voice' || m.class === 'voice2' ? OK : '#3987e5',
			})),
	);
	const egressMarks = $derived<TrendMark[]>(
		(egress?.marks ?? []).map((m) => ({
			at: m.at,
			label: m.label,
			color: m.why === 'red' ? BAD : m.why === 'stuck' ? WARN : '#7e7e7e',
		})),
	);
	const pointMarks = $derived<TrendMark[]>(
		(points?.marks ?? [])
			.filter((m) => (m.class === 'point-stand') === (scope === 'stand'))
			.map((m) => ({ at: m.at, label: m.label, color: m.why === 'red' ? BAD : OK })),
	);

	function linesOf(chart: TrunkWireChart | null, c: ChartSpec, allow: (key: string) => boolean): TrendSeries[] {
		return (chart?.lines ?? [])
			.filter((l) => l.group === c.group && allow(l.key))
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

	/** Sum of a group's values over the named targets; null when nobody measured. */
	function sumOf(chart: TrunkWireChart | null, group: string, keys: string[]): number | null {
		const lines = (chart?.lines ?? []).filter((l) => l.group === group && keys.includes(l.key));
		let sum = 0;
		let seen = false;
		for (const l of lines)
			for (const v of l.values)
				if (v !== null) {
					sum += v;
					seen = true;
				}
		return seen ? sum : null;
	}

	/** The worst single value of a group over the named targets. */
	function maxOf(chart: TrunkWireChart | null, group: string, keys: string[]): number | null {
		const lines = (chart?.lines ?? []).filter((l) => l.group === group && keys.includes(l.key));
		let top: number | null = null;
		for (const l of lines)
			for (const v of l.values)
				if (v !== null) top = top === null ? v : Math.max(top, v);
		return top;
	}

	const calls = $derived(SCOPE[scope].calls);
	const voiceKey = $derived(SCOPE[scope].voice);

	// The people's voice: the pool's own call of the two fates over the
	// window. This is the number the cascade is judged by.
	const voice = $derived.by(() => ({
		red: sumOf(data, 'call_red', calls),
		worst: maxOf(data, 'call_gap', calls),
		silent: maxOf(data, 'call_silent', calls),
		holes: maxOf(hearing, 'holes60_up', [voiceKey]),
		holesDown: maxOf(hearing, 'holes60_down', [voiceKey]),
		runs: (sumOf(hearing, 'runs2_60_up', [voiceKey]) ?? 0) + (sumOf(hearing, 'runs2_60_down', [voiceKey]) ?? 0),
	}));

	const border = $derived.by(() => {
		const list = routePoints.flatMap((r) => r.points);
		let up = 0;
		let sampled = 0;
		let silent = 0;
		for (const p of list) {
			if (!p.sample) continue;
			sampled++;
			if (p.sample.up > 0) up++;
			silent = Math.max(silent, p.sample.silentSeconds);
		}
		return { up, sampled, total: list.length, silent };
	});

	const moves = $derived((egress?.marks ?? []).filter((m) => m.why === 'red').length);

	type Tone = 'ok' | 'warn' | 'bad';
	const rankOf = (t: Tone) => (t === 'ok' ? 0 : t === 'warn' ? 1 : 2);

	// One word over the route: whether the cascade is fine, and what is not.
	const verdict = $derived.by(() => {
		const red = voice.red ?? 0;
		const worst = voice.worst ?? 0;
		const holes = Math.max(voice.holes ?? 0, voice.holesDown ?? 0);
		const tone = (bad: boolean, warn: boolean): Tone => (bad ? 'bad' : warn ? 'warn' : 'ok');
		const parts = [
			{ name: 'звонок', tone: voice.worst === null ? 'warn' : tone(worst >= 500 || red > 5, red > 0 || worst >= 150) },
			{ name: 'слух', tone: voice.holes === null ? 'warn' : tone(holes >= 5 || voice.runs > 3, holes >= 1 || voice.runs > 0) },
			{ name: 'точки', tone: border.sampled === 0 ? 'warn' : tone(border.up === 0, border.up < border.total || border.silent > 0) },
			{ name: 'выходы', tone: tone(false, moves > 0) },
		] as { name: string; tone: Tone }[];
		const first = parts.reduce((a, b) => (rankOf(b.tone) > rankOf(a.tone) ? b : a), parts[0]);
		const off = parts.filter((x) => x.tone !== 'ok').map((x) => x.name);
		return {
			tone: first.tone,
			text: first.tone === 'ok' ? 'каскад живой' : `${first.tone === 'bad' ? 'каскад сломан' : 'каскад держится'}: ${off.join(', ')}`,
		};
	});

	const topCharts = $derived([
		{
			group: 'call_gap',
			title: 'Голос: худшая пауза',
			hint: 'Самая длинная пауза звонка магистрали за отрезок, по пулу людей. Волна входа на :x0 бьёт по всем проводам сразу, пул от неё стоит секунды.',
			format: ms,
			lines: linesOf(data, { group: 'call_gap', title: '', hint: '', format: ms }, (k) => calls.includes(k)),
		},
		{
			group: 'call_red',
			title: 'Голос: красные минуты',
			hint: 'Минуты, где звонок молчал дольше 150 мс или терял больше 1%: по ним селектор решает, какой провод несёт голос.',
			format: count,
			lines: linesOf(data, { group: 'call_red', title: '', hint: '', format: count }, (k) => calls.includes(k)),
		},
	]);

	const wireCharts = $derived(
		WIRE_CHARTS.map((c) => ({ ...c, lines: linesOf(data, c, (k) => isStand(k) === (scope === 'stand')) })).filter((c) => c.lines.length > 0),
	);
	const hearingCharts = $derived(
		HEARING_CHARTS.map((c) => ({ ...c, lines: linesOf(hearing, c, (k) => isStand(k) === (scope === 'stand')) })).filter((c) => c.lines.length > 0),
	);
	const egressKeyOrder = $derived([...new Set((egress?.lines ?? []).map((l) => l.key))].sort());
	const egressCharts = $derived(
		EGRESS_CHARTS.map((c) => ({
			...c,
			lines: (egress?.lines ?? [])
				.filter((l) => l.group === c.group)
				.sort((a, b) => a.key.localeCompare(b.key))
				.map<TrendSeries>((l) => ({
					key: l.key,
					label: l.key,
					color: EGRESS_COLORS[egressKeyOrder.indexOf(l.key) % EGRESS_COLORS.length],
					values: l.values,
					format: c.format,
					primary: true,
				})),
		})).filter((c) => c.lines.length > 0),
	);
	const pointCharts = $derived(
		POINT_CHARTS.map((c) => ({
			...c,
			lines: (points?.lines ?? [])
				.filter((l) => l.group === c.group && l.key.endsWith(' (стенд)') === (scope === 'stand'))
				.sort((a, b) => a.key.localeCompare(b.key))
				.map<TrendSeries>((l, i) => ({
					key: l.key,
					label: l.key.replace(' (стенд)', ''),
					color: EGRESS_COLORS[i % EGRESS_COLORS.length],
					values: l.values,
					format: c.format,
					primary: true,
				})),
		})).filter((c) => c.lines.length > 0),
	);

	// Everything that moved the cascade, newest first: the selector's moves and
	// the border points coming and going.
	const events = $derived.by(() => {
		const out: { at: string; label: string; down: boolean }[] = [];
		for (const m of data?.marks ?? [])
			if (m.label.startsWith('труба') === (scope === 'stand')) out.push({ at: m.at, label: m.label, down: m.why === 'red' });
		for (const m of points?.marks ?? [])
			if ((m.class === 'point-stand') === (scope === 'stand')) out.push({ at: m.at, label: m.label, down: m.why === 'red' });
		return out.sort((a, b) => b.at.localeCompare(a.at));
	});
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
			<span class="verdict {verdict.tone}" title="Сводка по четырём числам ниже: голос людей, слух приложения, точки перехода и выходы приёмников."
				><i aria-hidden="true"></i>{verdict.text}</span
			>
			{#if USE_MOCK}<span class="muted">демоданные</span>{/if}
		</div>

		{#if !data && !loadError}
			<p class="muted">загрузка…</p>
		{:else if data}
			<div class="grid top">
				{#each topCharts as c (c.group)}
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
							{marks}
							height={200}
						/>
					</section>
				{/each}
			</div>
		{/if}

		{#if pointCharts.length && points}
			<h2 class="section">точки перехода</h2>
			<div class="grid">
				{#each pointCharts as c (c.group)}
					<section class="card">
						<h2 title={c.hint}>{c.title}</h2>
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
		{/if}

		{#if wireCharts.length && data}
			<h2 class="section">по проводам</h2>
			<div class="grid">
				{#each wireCharts as c (c.group)}
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
							marks={marks}
							height={190}
							empty="нет замеров за это окно"
						/>
					</section>
				{/each}
			</div>
		{/if}

		{#if hearingCharts.length && hearing}
			<h2 class="section">что слышит слушатель</h2>
			<div class="grid">
				{#each hearingCharts as c (c.group)}
					<section class="card">
						<h2 title={c.hint}>{c.title}</h2>
						<div class="legend">
							{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
						</div>
						<TrendChart
							buckets={hearing.buckets}
							bucketSeconds={hearing.bucketSeconds}
							series={c.lines}
							formatAxis={c.format}
							marks={marks}
							height={190}
							empty="слух не мерили за окно"
						/>
					</section>
				{/each}
			</div>
		{/if}

		{#if egressCharts.length && egress}
			<h2 class="section">выходы приёмников</h2>
			<div class="grid">
				{#each egressCharts as c (c.group)}
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
							marks={egressMarks}
							height={190}
							empty="сторож выходов молчал за окно"
						/>
					</section>
				{/each}
			</div>
		{/if}

		{#if events.length}
			<h2 class="section">события</h2>
			<ul class="events">
				{#each events as event (event.at + event.label)}
					<li class:down={event.down}>
						<time datetime={event.at}>{eventTime.format(new Date(event.at))}</time>
						<span>{event.label}</span>
					</li>
				{/each}
			</ul>
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
		padding: 8px 0 12px;
		font-size: 13px;
	}

	.verdict {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		cursor: help;
	}

	.verdict i {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: currentColor;
	}

	.verdict.ok {
		color: #199e70;
	}

	.verdict.warn {
		color: #c98500;
	}

	.verdict.bad {
		color: #e66767;
	}

	.err {
		color: #e66767;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 420px), 1fr));
		gap: 12px;
	}

	.grid.top {
		margin-bottom: 4px;
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

	h2.section {
		margin: 24px 0 10px;
		font-size: 16px;
		cursor: default;
	}

	.muted {
		color: var(--text-2);
		font-size: 12px;
	}

	.events {
		margin: 0;
		padding: 0;
		list-style: none;
		max-height: 420px;
		overflow-y: auto;
	}

	.events li {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 14px;
		padding: 6px 0;
		border-bottom: 1px solid var(--border);
		font-size: 13px;
	}

	.events li:last-child {
		border: 0;
	}

	.events time {
		color: #199e70;
		font-variant-numeric: tabular-nums;
	}

	.events .down time {
		color: #e66767;
	}
</style>
