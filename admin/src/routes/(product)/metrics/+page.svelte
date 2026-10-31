<script lang="ts">
	import Chart from '$components/product/Chart.svelte';
	import WinChips from '$components/product/WinChips.svelte';
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
