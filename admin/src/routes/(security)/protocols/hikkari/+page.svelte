<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import TrendChart, { type TrendSeries } from '$components/entry/TrendChart.svelte';
	import { fetchHikkariChart, type HikkariChart, type HikkariWindow } from '$lib/api/hikkari';

	// What Hikkari's own sessions saw (protocol v1.04): the relay counts the
	// stream channel by the kernel, the datagram channel and the voice copies
	// going up; the client's core counts the copies coming down and its calls.
	// Every chart is a history: there is no number without its line.

	const WINDOWS: HikkariWindow[] = ['6h', '24h', '7d'];
	const REFRESH_MS = 60_000;

	const pct = (v: number) => `${v.toFixed(1)}%`;
	const pm = (v: number) => `${v.toFixed(1)} на 1000`;
	const ms = (v: number) => `${Math.round(v)} мс`;
	const count = (v: number) => Math.round(v).toLocaleString('ru');

	interface LineMeta {
		label: string;
		color: string;
	}
	const META: Record<string, LineMeta> = {
		saved_down: { label: 'пришло только копией по Naive', color: '#199e70' },
		first_stream_down: { label: 'копия по Naive первая, к клиенту', color: '#3987e5' },
		first_stream_up: { label: 'копия по Naive первая, к релею', color: '#9085e9' },
		lead_stream_down: { label: 'Naive опередил, p50', color: '#3987e5' },
		lead_dgram_down: { label: 'датаграмма опередила, p50', color: '#c98500' },
		calls: { label: 'звонков', color: '#3987e5' },
		calls_short: { label: 'короче 10 с', color: '#c98500' },
		calls_one_sided: { label: 'без ответного потока', color: '#e66767' },
		trunk_eof: { label: 'закрыт штатно', color: '#199e70' },
		trunk_reset: { label: 'сброс', color: '#e66767' },
		trunk_idle: { label: 'таймаут', color: '#c98500' },
		trunk_error: { label: 'ошибка', color: '#d55181' },
		trunk_retrans: { label: 'ретрансмиты членов пула', color: '#e66767' },
		trunk_rtt: { label: 'RTT членов пула, p95', color: '#3987e5' },
		naive_retrans: { label: 'ретрансмиты канала Naive', color: '#e66767' },
		naive_rtt: { label: 'RTT канала Naive, p95', color: '#3987e5' },
		dgram_loss_up: { label: 'датаграммы к релею', color: '#3987e5' },
		voice_loss_up: { label: 'голос к релею', color: '#199e70' },
		dgram_loss_down: { label: 'датаграммы к клиенту', color: '#c98500' },
		tunnels_eof: { label: 'штатно', color: '#199e70' },
		tunnels_reset: { label: 'сброс', color: '#e66767' },
		tunnels_idle: { label: 'таймаут', color: '#c98500' },
		tunnels_revoked: { label: 'ключ отозван', color: '#9085e9' },
		tunnels_stop: { label: 'рестарт релея', color: '#d55181' },
		tunnels_error: { label: 'ошибка', color: '#7e7e7e' },
	};

	interface ChartSpec {
		group: string;
		title: string;
		hint: string;
		format: (v: number) => string;
		max?: number;
	}
	const CHARTS: ChartSpec[] = [
		{
			group: 'dup_saved',
			title: 'Копия голоса по Naive: сколько пакетов спасла',
			hint: 'доля пакетов звонка, которые дошли до клиента только копией по Naive: датаграмма потерялась. Около нуля: копия не окупается.',
			format: pct,
		},
		{
			group: 'dup_first',
			title: 'Копия голоса по Naive: как часто приходит первой',
			hint: 'доля пакетов звонка, у которых первой пришла копия по Naive, а не датаграмма.',
			format: pct,
			max: 100,
		},
		{
			group: 'dup_lead',
			title: 'На сколько первая копия опережает вторую',
			hint: 'медиана: насколько раньше пришла первая копия пакета звонка.',
			format: ms,
		},
		{
			group: 'calls',
			title: 'Детектор звонка',
			hint: 'звонки, которые узнал клиент. Короткие и без ответного потока, вероятно, не звонки (игра, всплеск мелких пакетов).',
			format: count,
		},
		{
			group: 'trunk_ends',
			title: 'Члены пула каскада: как закончились',
			hint: 'соединения пула входа на выходе каскада, по тому, как закрылись.',
			format: count,
		},
		{
			group: 'trunk_tcp',
			title: 'Члены пула: ретрансмиты',
			hint: 'сколько сегментов из 1000 выход отправил членам пула повторно: потери на пути вход ↔ выход.',
			format: pm,
		},
		{ group: 'trunk_rtt', title: 'Члены пула: задержка', hint: 'RTT соединений пула по ядру выхода.', format: ms },
		{
			group: 'naive_tcp',
			title: 'Канал Naive: ретрансмиты',
			hint: 'сколько сегментов из 1000 релей отправил клиентам повторно: потери на пути клиент ↔ релей.',
			format: pm,
		},
		{
			group: 'naive_rtt',
			title: 'Канал Naive: задержка',
			hint: 'RTT TCP-соединений клиентов по ядру релея.',
			format: ms,
		},
		{
			group: 'quic_loss',
			title: 'Канал датаграмм: потери',
			hint: 'сколько QUIC-пакетов из 1000 потерялось: клиент считает к релею, релей к клиенту.',
			format: pm,
		},
		{
			group: 'tunnel_ends',
			title: 'Туннели Naive: как закончились',
			hint: 'туннели CONNECT клиентов, по тому, как закрылись. Рестарты релея видны отдельной линией.',
			format: count,
		},
	];

	let data = $state<HikkariChart | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<HikkariWindow>('24h');
	let timer: ReturnType<typeof setInterval> | undefined;

	async function load() {
		try {
			data = await fetchHikkariChart(window);
			loadError = null;
		} catch (e) {
			loadError = e instanceof Error ? e.message : 'не удалось загрузить';
		}
	}

	function pick(w: HikkariWindow) {
		if (w === window) return;
		window = w;
		void load();
	}

	onMount(() => {
		void load();
		timer = setInterval(load, REFRESH_MS);
	});
	onDestroy(() => clearInterval(timer));

	const charts = $derived(
		CHARTS.map((c) => ({
			...c,
			lines: (data?.lines ?? [])
				.filter((l) => l.group === c.group)
				.map<TrendSeries>((l, i) => ({
					key: l.key,
					label: META[l.key]?.label ?? l.key,
					color: META[l.key]?.color ?? '#7e7e7e',
					values: l.values,
					format: c.format,
					primary: i === 0,
				})),
		})),
	);
</script>

<PageShell>
	<div class="page">
		<div class="head">
			<h1>hikkari изнутри</h1>
			<div class="chips">
				<a class="chip" href="/protocols">у тестеров</a>
				{#each WINDOWS as w (w)}
					<button class="chip" class:on={window === w} onclick={() => pick(w)}>{w}</button>
				{/each}
			</div>
		</div>
		<p class="hint">
			Что видели сами сессии протокола: релей считает канал Naive по ядру и копии голоса к себе, ядро клиента копии к
			себе и звонки. Клиентские линии только у тестеров с ядром hk.8 и новее.
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
							max={c.max}
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
