<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import TrendChart, { type TrendMark, type TrendSeries } from '$components/entry/TrendChart.svelte';
	import { describeApiError } from '$lib/api/client';
	import {
		fetchRelayRunChart,
		type RelayKnobEvent,
		type RelayRunChart,
		type RelayRunSeries,
		type RelayRunWindow,
	} from '$lib/api/relayRuns';

	// One knob of a relay's config over time (plans/2026-11-04-relay-search-method.md):
	// a line per value of the knob and per vantage — the matrix stand from SPb
	// and Latvia, each tester on their own provider. Measured the way
	// [diag:load] measures; a bucket nobody measured is a gap, not a zero.

	const WINDOWS: RelayRunWindow[] = ['24h', '7d', '30d'];
	const KNOB_LABEL: Record<string, string> = {
		fp: 'отпечаток',
		sni: 'SNI',
		port: 'порт',
		flow: 'flow',
		alpn: 'ALPN',
	};
	const VANTAGE_LABEL: Record<string, string> = {
		spb: 'СПб',
		lv: 'Латвия',
		'ru-spb': 'СПб, стенд по расписанию',
		'lv-main': 'Латвия, стенд по расписанию',
	};
	const EVENT_COLOR: Record<RelayKnobEvent['kind'], string> = {
		switch: '#e8e8e8',
		all_red: '#e66767',
		address_cut: '#c98500',
	};
	const COLORS = ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9', '#e66767'];
	const BASELINE_COLOR = '#71717a';
	const REFRESH_MS = 120_000;

	let data = $state<RelayRunChart | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<RelayRunWindow>('24h');
	let knob = $state('fp');
	let profile = $state('');
	let timer: ReturnType<typeof setInterval> | undefined;

	async function load() {
		try {
			data = await fetchRelayRunChart(window, knob, profile);
			if (!profile) profile = data.profile;
			loadError = null;
		} catch (e) {
			loadError = describeApiError(e);
		}
	}

	function set(next: { window?: RelayRunWindow; knob?: string; profile?: string }) {
		if (next.window) window = next.window;
		if (next.knob) knob = next.knob;
		if (next.profile !== undefined) profile = next.profile;
		void load();
	}

	onMount(() => {
		void load();
		timer = setInterval(load, REFRESH_MS);
	});
	onDestroy(() => clearInterval(timer));

	const percent = (v: number) => `${Math.round(v)}%`;

	// A value keeps its colour in every chart and from every vantage; the
	// vantage is in the name. The baseline (no protocol) is grey.
	const colorOf = $derived.by(() => {
		const values = [...new Set((data?.series ?? []).map((s) => s.value))].filter((v) => v !== 'no-protocol').sort();
		return (v: string) => (v === 'no-protocol' ? BASELINE_COLOR : COLORS[values.indexOf(v) % COLORS.length]);
	});

	function vantageName(s: RelayRunSeries): string {
		return VANTAGE_LABEL[s.vantage] ?? s.vantage;
	}

	function valueName(v: string): string {
		if (v === 'no-protocol') return 'без протокола';
		if (v === '?') return 'не известно';
		return v;
	}

	function lines(pick: (s: RelayRunSeries) => (number | null)[]): TrendSeries[] {
		return (data?.series ?? []).map((s) => ({
			key: s.key,
			label: `${valueName(s.value)} · ${vantageName(s)}`,
			color: colorOf(s.value),
			values: pick(s),
			format: percent,
		}));
	}

	// What the knob controller did, at its moment on every chart: a switch, or
	// a state it could not act on.
	function eventLabel(e: RelayKnobEvent): string {
		switch (e.kind) {
			case 'switch':
				return `контроллер: ${e.from} → ${e.to}. ${e.reason ?? ''}`;
			case 'all_red':
				return `контроллер: все кандидаты красные. ${e.reason ?? ''}`;
			default:
				return `контроллер: режут адрес. ${e.reason ?? ''}`;
		}
	}

	const marks = $derived<TrendMark[]>(
		(data?.events ?? []).map((e) => ({ at: e.at, label: eventLabel(e), color: EVENT_COLOR[e.kind] })),
	);

	const charts = $derived([
		{ title: '50 соединений разом: ответило', lines: lines((s) => s.burstPct) },
		{ title: '3 запроса после всплеска: ответило', lines: lines((s) => s.afterPct) },
		{ title: '1 МБ одним потоком: пришёл целиком', lines: lines((s) => s.dlOkPct) },
	]);
</script>

<PageShell>
	<div class="page">
		<div class="head">
			<h1>ручки релеев</h1>
			<div class="chips">
				<a class="chip" href="/protocols">у тестеров</a>
				{#each WINDOWS as w (w)}
					<button class="chip" class:on={window === w} onclick={() => set({ window: w })}>{w}</button>
				{/each}
			</div>
		</div>

		{#if loadError}
			<p class="err">{loadError}</p>
		{/if}

		{#if !data && !loadError}
			<p class="hint">Загрузка…</p>
		{:else if data}
			<div class="head">
				<div class="chips">
					{#each data.knobs as k (k)}
						<button class="chip" class:on={knob === k} onclick={() => set({ knob: k })}>{KNOB_LABEL[k] ?? k}</button>
					{/each}
				</div>
				<div class="chips">
					{#each data.profiles as p (p)}
						<button class="chip" class:on={profile === p} onclick={() => set({ profile: p })}>{p}</button>
					{/each}
				</div>
			</div>

			{#if data.series.length === 0}
				<p class="hint">за окно нет ни одного прогона{profile ? ` профиля ${profile}` : ''}</p>
			{/if}

			{#each charts as c (c.title)}
				<section class="card">
					<h2>{c.title}</h2>
					<div class="legend">
						{#each c.lines as s (s.key)}<span><i style="background: {s.color}"></i>{s.label}</span>{/each}
					</div>
					<TrendChart
						buckets={data.buckets}
						bucketSeconds={data.bucketSeconds}
						series={c.lines}
						formatAxis={percent}
						max={100}
						height={200}
						{marks}
					/>
				</section>
			{/each}

			<p class="hint">
				Строки пишут стенд по расписанию на hub (раз в 30 минут с <code>ru-spb</code> и <code>lv-main</code>), ручной
				стенд
				<code>ops matrix</code> (СПб и Латвия) и раунды <code>[diag:load]</code> тестеров раз в 30 минут. Вертикальные отметки
				— журнал контроллера ручек: белая — переключение ноды, красная — все кандидаты красные, жёлтая — режут адрес. Значение
				ручки у тестера берётся из его конфига; «не известно» значит, что конфиг в логе обрезан до профиля. Опыт меняет одну
				ручку: линии разных ручек одного профиля не сравнивают между собой.
			</p>
		{/if}
	</div>
</PageShell>

<style>
	.page {
		max-width: 1200px;
		line-height: 1.5;
	}

	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 8px 12px;
		margin-bottom: 12px;
	}

	h1 {
		margin: 0;
		font-size: 18px;
		font-weight: 600;
	}

	h2 {
		margin: 0 0 8px;
		font-size: 14px;
		font-weight: 600;
	}

	.err {
		margin: 0 0 12px;
		color: var(--bad);
	}

	.hint {
		margin: 0 0 12px;
		color: var(--text-2);
		font-size: 12px;
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
		cursor: pointer;
		text-decoration: none;
		display: inline-flex;
		align-items: center;
	}

	.chip.on {
		background: oklch(0.26 0 0);
		color: var(--text);
	}

	.card {
		min-width: 0;
		margin-bottom: 12px;
		padding: 16px;
		background: oklch(0.2 0 0);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 14px;
		margin-bottom: 8px;
		font-size: 11px;
		color: var(--text-2);
		font-family: var(--mono);
	}

	.legend span {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}

	.legend i {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 2px;
	}

	code {
		font-family: var(--mono);
	}
</style>
