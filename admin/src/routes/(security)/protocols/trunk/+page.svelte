<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import TrendChart, { type TrendMark, type TrendSeries } from '$components/entry/TrendChart.svelte';
	import { fetchTrunkWires, type TrunkWindow, type TrunkWireChart } from '$lib/api/trunkWires';

	// The trunk's wires through the border (stage 4): the entry's call over
	// each wire every 20 ms and its 1 MiB every 10 minutes, as history. The
	// pool's own call (copies1, copies2), the stand's process per wire
	// (wire-*) and the entry's own wires (trunk-*) side by side; the
	// selector's moves of voice and bulk are marks on every chart.
	// Only charts: there is no number without its line.

	const WINDOWS: TrunkWindow[] = ['6h', '24h', '7d'];
	const REFRESH_MS = 60_000;

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
	};

	function wireOf(key: string): string {
		if (key.startsWith('copies')) return 'pool';
		return key.replace(/^(wire|trunk)-/, '');
	}

	function labelOf(key: string): string {
		if (key === 'copies1') return 'пул, звонок 1 копией';
		if (key === 'copies2') return 'пул, звонок 2 копиями';
		if (key.startsWith('wire-')) return `стенд ${key.slice(5)}`;
		if (key === 'trunk-pool') return 'вход, пул';
		if (key.startsWith('trunk-')) return `вход ${key.slice(6)}`;
		return key;
	}

	function colorOf(key: string): string {
		const c = WIRE_COLOR[wireOf(key)] ?? '#7e7e7e';
		// The pool's call with one copy is the lighter of its two lines.
		return key === 'copies1' ? '#d55181' : c;
	}

	// Lines in a fixed order: the pool first, then the entry's wires, then
	// the stand's.
	function rank(key: string): string {
		const head = key.startsWith('copies') ? '0' : key.startsWith('trunk-') ? '1' : '2';
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

	let data = $state<TrunkWireChart | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<TrunkWindow>('24h');
	let timer: ReturnType<typeof setInterval> | undefined;

	async function load() {
		try {
			data = await fetchTrunkWires(window);
			loadError = null;
		} catch (e) {
			loadError = e instanceof Error ? e.message : 'не удалось загрузить';
		}
	}

	function pick(w: TrunkWindow) {
		if (w === window) return;
		window = w;
		void load();
	}

	onMount(() => {
		void load();
		timer = setInterval(load, REFRESH_MS);
	});
	onDestroy(() => clearInterval(timer));

	const marks = $derived<TrendMark[]>(
		(data?.marks ?? []).map((m) => ({
			at: m.at,
			label: m.label,
			color: m.why === 'red' ? '#e66767' : m.class === 'voice' ? '#199e70' : '#3987e5',
		})),
	);

	const charts = $derived(
		CHARTS.map((c) => ({
			...c,
			lines: (data?.lines ?? [])
				.filter((l) => l.group === c.group)
				.sort((a, b) => rank(a.key).localeCompare(rank(b.key)))
				.map<TrendSeries>((l) => ({
					key: l.key,
					label: labelOf(l.key),
					color: colorOf(l.key),
					values: l.values,
					format: c.format,
					primary: l.key.startsWith('trunk-'),
				})),
		})),
	);
</script>

<PageShell>
	<div class="page">
		<div class="head">
			<h1>провода магистрали</h1>
			<div class="chips">
				<a class="chip" href="/protocols/hikkari">hikkari изнутри</a>
				{#each WINDOWS as w (w)}
					<button class="chip" class:on={window === w} onclick={() => pick(w)}>{w}</button>
				{/each}
			</div>
		</div>
		<p class="hint">
			Участок РФ ДЦ → ЕУ ДЦ: вход меряет каждый провод своим звонком (датаграмма раз в 20 мс) и загрузкой 1 МиБ раз в
			10 минут. Пул: звонок магистрали по пулу, стенд: отдельный процесс провода на входе, вход: провода в самом процессе
			входа. Отметки на оси: селектор перевёл голос или объём на другой провод (красная: провод покраснел).
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
</style>
