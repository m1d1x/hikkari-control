<script lang="ts">
	import Chart from '$components/product/Chart.svelte';
	import WinChips from '$components/product/WinChips.svelte';
	import EChart from '$components/charts/EChart.svelte';
	import EmptyChart from '$components/charts/EmptyChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';
	import { fetchProductMetrics, type MetricsShare } from '$lib/api/product';
	import { num, pct, secs } from '$lib/product/format';
	import { Windowed } from '$lib/product/windowed.svelte';

	// Counted from the clients' telemetry; a person is an install. Nulls are
	// figures with nothing behind them yet and read as «—», never as 0.
	const m = new Windowed('7d', fetchProductMetrics);
	const d = $derived(m.data);

	const OS_COLOR: Record<string, string> = {
		windows: 'var(--blue)',
		macos: 'var(--text)',
		linux: 'var(--warn)',
		android: 'var(--ok)',
		ios: 'var(--violet)',
	};
	const VERSION_COLORS = ['var(--primary)', 'var(--blue)', 'var(--violet)', 'var(--ok)', 'var(--warn)'];

	const or = (v: number | null, f: (v: number) => string) => (v === null ? '—' : f(v));
	const osColor = (s: MetricsShare) => OS_COLOR[s.key] ?? 'var(--text-3)';
	const versionColor = (i: number) => VERSION_COLORS[i] ?? 'var(--text-3)';

	function makeChart(
		data: number[],
		labels: string[],
		color: string,
		opts?: { compact?: boolean; unit?: string },
	): EChartsOption {
		const compact = opts?.compact ?? true;
		const labelInterval = compact
			? Math.max(Math.floor(labels.length / 8) - 1, 0)
			: Math.max(Math.floor(labels.length / 6) - 1, 0);
		return {
			grid: { left: compact ? 36 : 48, right: 12, top: 12, bottom: 24 },
			xAxis: {
				type: 'category',
				data: labels,
				axisLabel: {
					show: true,
					fontSize: 10,
					color: '#787878',
					interval: labelInterval,
				},
				axisLine: { lineStyle: { color: '#3a3a3a' } },
				axisTick: { show: false },
			},
			yAxis: {
				type: 'value',
				min: 0,
				axisLabel: { fontSize: 10, color: '#787878' },
				splitLine: { lineStyle: { color: '#2c2c2c' } },
			},
			tooltip: {
				trigger: 'axis',
				backgroundColor: '#262626',
				borderColor: '#3a3a3a',
				textStyle: { color: '#ededed', fontSize: 12 },
				formatter: opts?.unit
					? (params: any) => {
							const p = Array.isArray(params) ? params[0] : params;
							return `${p.axisValue}<br/>${p.value}${opts.unit}`;
						}
					: undefined,
			},
			series: [
				{
					type: 'line',
					data,
					smooth: false,
					symbol: 'none',
					lineStyle: { width: 1.5, color },
					areaStyle: { opacity: 0.1, color },
				},
			],
		};
	}

	let dauData = $derived(d?.people?.dauTimeSeries?.data ?? []);
	let dauLabels = $derived(dauData.map((pt) => (pt.timestamp.length > 5 ? pt.timestamp.slice(5) : pt.timestamp)));
	let dauValues = $derived(dauData.map((pt) => pt.value));
	let dauChart = $derived(makeChart(dauValues, dauLabels, '#45b97c'));

	let wauData = $derived(d?.people?.wauTimeSeries?.data ?? []);
	let wauLabels = $derived(wauData.map((pt) => (pt.timestamp.length > 5 ? pt.timestamp.slice(5) : pt.timestamp)));
	let wauValues = $derived(wauData.map((pt) => pt.value));
	let wauChart = $derived(makeChart(wauValues, wauLabels, '#6192d4'));

	let mauData = $derived(d?.people?.mauTimeSeries?.data ?? []);
	let mauLabels = $derived(mauData.map((pt) => pt.timestamp));
	let mauValues = $derived(mauData.map((pt) => pt.value));
	let mauChart = $derived(makeChart(mauValues, mauLabels, '#b07cd8'));

	let stickData = $derived(d?.people?.stickinessTimeSeries?.data ?? []);
	let stickLabels = $derived(stickData.map((pt) => (pt.timestamp.length > 5 ? pt.timestamp.slice(5) : pt.timestamp)));
	let stickValues = $derived(stickData.map((pt) => pt.value));
	let stickinessChart = $derived(makeChart(stickValues, stickLabels, '#e8a838', { unit: '%' }));
</script>

<div class="head">
	<h1>обзор</h1>
	{#if m.loading && d}<span class="dim">обновляю…</span>{/if}
	<WinChips bind:value={m.win} />
</div>

{#if m.error}
	<p class="warn">не загрузилось: {m.error}</p>
{/if}

{#if d}
	<section class="audience-section">
		<div class="audience-grid">
			<div class="chart-card">
				<div class="chart-header">
					<div>
						<h3 class="chart-title">DAU</h3>
						<span class="chart-hint">уникальные за сутки · 30 дней</span>
					</div>
					<span class="chart-value mono">{num(d.people.dau)}</span>
				</div>
				{#if dauValues.length >= 1}
					<EChart option={dauChart} height="150px" />
				{:else}
					<EmptyChart note="Нет данных" height="150px" />
				{/if}
			</div>

			<div class="chart-card">
				<div class="chart-header">
					<div>
						<h3 class="chart-title">WAU</h3>
						<span class="chart-hint">уникальные за неделю · 12 недель</span>
					</div>
					<span class="chart-value mono">{num(d.people.wau)}</span>
				</div>
				{#if wauValues.length >= 1}
					<EChart option={wauChart} height="150px" />
				{:else}
					<EmptyChart note="Нет данных" height="150px" />
				{/if}
			</div>

			<div class="chart-card">
				<div class="chart-header">
					<div>
						<h3 class="chart-title">MAU</h3>
						<span class="chart-hint">уникальные за месяц · 6 месяцев</span>
					</div>
					<span class="chart-value mono">{num(d.people.mau)}</span>
				</div>
				{#if mauValues.length >= 1}
					<EChart option={mauChart} height="150px" />
				{:else}
					<EmptyChart note="Нет данных" height="150px" />
				{/if}
			</div>

			<div class="chart-card">
				<div class="chart-header">
					<div>
						<h3 class="chart-title">Липкость</h3>
						<span class="chart-hint">DAU / MAU · 30 дней</span>
					</div>
					<span class="chart-value mono">{or(d.people.stickiness, pct)}</span>
				</div>
				{#if stickValues.length >= 1}
					<EChart option={stickinessChart} height="150px" />
				{:else}
					<EmptyChart note="Нет данных" height="150px" />
				{/if}
			</div>
		</div>
	</section>

	<section class="card">
		<div class="split">
			<div>
				<div class="big">{num(d.people.active)}</div>
				<div class="what"><i class="sw" style="background: var(--primary)"></i>активных установок</div>
				<div class="breakdown">
					<div><span><i class="sw" style="background: var(--ok)"></i>новых</span><b>{num(d.people.fresh)}</b></div>
					<div><span>вернулись</span><b>{num(d.people.returning)}</b></div>
					<div class="sep"><span>установок за всё время</span><b>{num(d.people.total)}</b></div>
				</div>
			</div>
			<Chart
				buckets={d.buckets}
				win={m.win}
				series={[
					{ label: 'активные', color: 'var(--primary)', values: d.people.series.active, primary: true },
					{ label: 'новые', color: 'var(--ok)', values: d.people.series.fresh },
				]}
			/>
		</div>
		<div class="strip">
			<span>попыток подключения<b>{num(d.connect.attempts)}</b></span>
			<span>успешно<b>{or(d.connect.okPct, pct)}</b></span>
			<span>подключение, медиана<b>{or(d.connect.secsP50, secs)}</b></span>
			<span>в туннеле за день, медиана<b>{or(d.connect.tunnelMinutesP50, (v) => `${Math.round(v)} мин`)}</b></span>
		</div>
	</section>

	<section class="card">
		<h2>первое подключение</h2>
		<div class="strip">
			<span>новых установок<b>{num(d.activation.installs)}</b></span>
			<span>дошли до рабочего туннеля<b>{num(d.activation.connected)}</b></span>
			<span>доля<b>{or(d.activation.pct, pct)}</b></span>
			<span>от установки до туннеля, медиана<b>{or(d.activation.secsP50, secs)}</b></span>
		</div>
	</section>

	<section class="card">
		<div class="split">
			<div>
				<div class="big">{or(d.downloads.clicks, num)}</div>
				<div class="what"><i class="sw" style="background: var(--blue)"></i>кликов «скачать»</div>
				<div class="breakdown">
					<div><span><i class="sw" style="background: var(--violet)"></i>первых запусков</span><b>{num(d.downloads.installs)}</b></div>
					<div class="sep"><span>клик → запуск</span><b>{or(d.downloads.convPct, pct)}</b></div>
				</div>
				{#if d.downloads.clicks === null}
					<div class="dim note">лендинг пока не передаёт клики</div>
				{/if}
			</div>
			<Chart
				buckets={d.buckets}
				win={m.win}
				series={[
					...(d.downloads.clicksSeries
						? [{ label: 'клики', color: 'var(--blue)', values: d.downloads.clicksSeries, primary: true }]
						: []),
					{ label: 'запуски', color: 'var(--violet)', values: d.people.series.fresh, primary: !d.downloads.clicksSeries },
				]}
			/>
		</div>
		{#if d.downloads.platforms.length > 0}
			<div class="strip">
				<div class="bar">
					{#each d.downloads.platforms as p (p.key)}
						<i style="width: {p.pct}%; background: {osColor(p)}"></i>
					{/each}
				</div>
				{#each d.downloads.platforms as p (p.key)}
					<span><i class="sw" style="background: {osColor(p)}"></i> {p.key}<b>{num(p.installs)}</b><span class="dim">&nbsp;· {p.pct.toFixed(0)}%</span></span>
				{/each}
			</div>
		{/if}
	</section>

	{@render shares('платформы', d.platforms, osColor)}
	{@render shares('версии', d.versions, (_s, i) => versionColor(i))}
{:else if m.loading}
	<p class="empty">загружаю…</p>
{/if}

{#snippet shares(title: string, list: MetricsShare[], color: (s: MetricsShare, i: number) => string)}
	<section class="card">
		<h2>{title}</h2>
		{#if list.length === 0}
			<p class="empty">за окно ничего не пришло</p>
		{:else}
			<div class="strip">
				<div class="bar">
					{#each list as s, i (s.key)}
						<i style="width: {s.pct}%; background: {color(s, i)}"></i>
					{/each}
				</div>
				{#each list as s, i (s.key)}
					<span><i class="sw" style="background: {color(s, i)}"></i> {s.key}<b>{num(s.installs)}</b><span class="dim">&nbsp;· {s.pct.toFixed(0)}%</span></span>
				{/each}
			</div>
		{/if}
	</section>
{/snippet}

<style>
	.audience-section {
		margin-bottom: 24px;
	}

	.audience-grid {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 16px;
	}

	.chart-card {
		background: var(--card);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 16px;
		display: flex;
		flex-direction: column;
	}

	.chart-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		margin-bottom: 12px;
	}

	.chart-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
		margin: 0;
	}

	.chart-hint {
		font-size: 11px;
		color: var(--text-3);
		display: block;
		margin-top: 2px;
	}

	.chart-value {
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-size: 22px;
		font-weight: 600;
		letter-spacing: -0.02em;
		color: var(--text);
		line-height: 1.1;
	}

	@media (max-width: 1100px) {
		.audience-grid {
			grid-template-columns: repeat(2, 1fr);
			gap: 14px;
		}
	}

	@media (max-width: 640px) {
		.audience-grid {
			grid-template-columns: 1fr;
			gap: 12px;
		}
	}

	.note {
		margin-top: 10px;
		font-size: 11px;
	}

	.bar {
		flex-basis: 100%;
		display: flex;
		gap: 2px;
		height: 6px;
		margin: 2px 0 4px;
		border-radius: 3px;
		overflow: hidden;
	}

	.bar i {
		opacity: 0.8;
	}
</style>
