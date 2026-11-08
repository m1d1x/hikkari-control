<script lang="ts">
	import { formatRate } from '$lib/utils/rate';
	import { onDestroy, onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import ProtoBadge from '$components/entry/ProtoBadge.svelte';
	import TrendChart from '$components/entry/TrendChart.svelte';
	import { describeEntryError, formatCents, PROTO_LABEL, providerLabel } from '$lib/api/entry';
	import { fetchFleetProfiles, type FleetProfile, type FleetProfiles, type FleetWindow } from '$lib/api/fleet';

	// Everything subscribers are handed right now, one card per profile: what
	// it is (hoster, place, price, traffic, disguise) and how it lived over the
	// window: ping from RU, and what the relay passed for subscribers.

	const WINDOWS: FleetWindow[] = ['24h', '7d', '30d'];
	const REFRESH_MS = 60_000;
	const AVAIL_COLOR = '#d95926';
	const RTT_COLOR = '#3987e5';
	const SPEED_COLOR = '#199e70';

	let data = $state<FleetProfiles | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<FleetWindow>('24h');
	let proto = $state<string>('all');
	let timer: ReturnType<typeof setInterval> | undefined;

	async function load() {
		try {
			data = await fetchFleetProfiles(window);
			loadError = null;
		} catch (e) {
			loadError = describeEntryError(e);
		}
	}

	function pick(w: FleetWindow) {
		if (w === window) return;
		window = w;
		void load();
	}

	onMount(() => {
		void load();
		timer = setInterval(load, REFRESH_MS);
	});
	onDestroy(() => clearInterval(timer));

	const protos = $derived([...new Set((data?.profiles ?? []).map((p) => p.proto))]);
	const shown = $derived((data?.profiles ?? []).filter((p) => proto === 'all' || p.proto === proto));

	const pct = (v: number) => `${v.toLocaleString('ru', { maximumFractionDigits: 1 })}%`;
	const ms = (v: number) => `${Math.round(v)} мс`;

	/** Gigabytes said in the unit that reads best: 1,2 ТБ, 340 ГБ. */
	function size(gb: number): string {
		// A hoster that sells a terabyte as 1024 GB (VDSina: 32768) means a
		// round number of them; one that writes 10000 means ten.
		const tb = gb % 1024 === 0 ? gb / 1024 : gb / 1000;
		if (gb >= 1000) return `${tb.toLocaleString('ru', { maximumFractionDigits: 1 })} ТБ`;
		return `${gb.toLocaleString('ru', { maximumFractionDigits: gb < 10 ? 1 : 0 })} ГБ`;
	}

	/** This month's traffic against the plan's: «1,2 ТБ из 32 ТБ». */
	function traffic(p: FleetProfile): string | null {
		if (p.trafficUsedGb == null && p.trafficQuotaGb == null) return null;
		const used = p.trafficUsedGb == null ? '?' : size(p.trafficUsedGb);
		return p.trafficQuotaGb == null ? used : `${used} из ${size(p.trafficQuotaGb)}`;
	}

	/** "Amsterdam 1, nl" and "Amsterdam, NL" read the same: city, then country code. */
	function geo(location: string): string {
		const [city, country] = location.split(', ');
		const place = city.replace(/\s+\d+$/, '');
		return country ? `${place}, ${country.toUpperCase()}` : place;
	}

	/**
	 * The card's list. Every card has every row, in one order and one type, so
	 * cards side by side line up; what could not be read is a dash.
	 */
	function rows(p: FleetProfile): [string, string][] {
		return [
			['хостер', providerLabel(p.provider)],
			['гео', geo(p.location)],
			['цена', p.monthlyCents != null ? `${formatCents(p.monthlyCents)}/мес` : '-'],
			['трафик', traffic(p) ?? '-'],
			['домен', p.cover || '-'],
			['отпечаток', p.fingerprint || '-'],
			['в выдаче', age(p.boundAt)],
		];
	}

	/** How long the profile has been handed out: «32 д», «5 ч». */
	function age(iso: string): string {
		const hours = Math.max(0, (Date.now() - new Date(iso).getTime()) / 3_600_000);
		return hours < 24 ? `${Math.floor(hours)} ч` : `${Math.floor(hours / 24)} д`;
	}
</script>

<PageShell>
	<div class="fleet-page">
		<div class="head">
			<h1>флот</h1>
			<div class="chips">
				{#each WINDOWS as w (w)}
					<button class="chip" class:on={window === w} onclick={() => pick(w)}>{w}</button>
				{/each}
			</div>
		</div>

		{#if loadError}
			<p class="err">{loadError}</p>
		{/if}

		{#if !data && !loadError}
			<p class="hint">Загрузка…</p>
		{:else if data}
			{#if protos.length > 1}
				<div class="chips filters">
					<button class="chip" class:on={proto === 'all'} onclick={() => (proto = 'all')}>все протоколы</button>
					{#each protos as p (p)}
						<button class="chip" class:on={proto === p} onclick={() => (proto = p)}>{PROTO_LABEL[p] ?? p}</button>
					{/each}
				</div>
			{/if}

			{#if shown.length === 0}
				<p class="hint">На раздаче нет ни одного профиля.</p>
			{/if}

			<div class="grid">
				{#each shown as p (p.slug)}
					<section class="card">
						<header>
							<h2>{p.label}</h2>
							<ProtoBadge proto={p.proto} />
						</header>
						<dl>
							{#each rows(p) as [label, value] (label)}
								<dt>{label}</dt>
								<dd>{value}</dd>
							{/each}
						</dl>

						<TrendChart
							buckets={data.buckets}
							bucketSeconds={data.bucketSeconds}
							series={[
								{
									key: 'avail',
									label: 'доступность',
									color: AVAIL_COLOR,
									values: p.availPct,
									format: pct,
									primary: true,
								},
							]}
							formatAxis={pct}
							max={100}
							height={120}
							empty="пробники не судили за окно"
						/>
						<TrendChart
							buckets={data.buckets}
							bucketSeconds={data.bucketSeconds}
							series={[{ key: 'rtt', label: 'пинг', color: RTT_COLOR, values: p.rttMs, format: ms, primary: true }]}
							formatAxis={ms}
							height={120}
							empty="пинга нет за окно"
						/>
						<TrendChart
							buckets={data.buckets}
							bucketSeconds={data.bucketSeconds}
							series={[
								{
									key: 'speed',
									label: 'скорость с пробников РФ',
									color: SPEED_COLOR,
									values: p.speedMbps,
									format: formatRate,
									primary: true,
								},
							]}
							formatAxis={formatRate}
							height={120}
							empty="пробники не мерили скорость за окно"
						/>
					</section>
				{/each}
			</div>

			{#each data.missing as m (m.figure)}
				<p class="hint">Нет данных ({m.figure}): {m.why}</p>
			{/each}
		{/if}
	</div>
</PageShell>

<style>
	.fleet-page {
		color: var(--text, oklch(0.93 0 0));
		font-size: 13px;
		line-height: 1.5;
	}

	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		margin-bottom: 12px;
	}

	h1 {
		margin: 0;
		font-size: 18px;
		font-weight: 600;
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}

	.filters {
		margin-bottom: 12px;
	}

	.chip {
		min-height: 32px;
		padding: 4px 10px;
		border: 1px solid oklch(0.24 0 0);
		border-radius: 6px;
		background: none;
		color: oklch(0.62 0 0);
		font: inherit;
		font-size: 12px;
		cursor: pointer;
	}

	.chip.on {
		background: oklch(0.26 0 0);
		color: oklch(0.93 0 0);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 440px), 1fr));
		gap: 12px;
		margin-bottom: 12px;
	}

	.card {
		min-width: 0;
		padding: 12px 14px 10px;
		background: oklch(0.2 0 0);
		border: 1px solid oklch(0.24 0 0);
		border-radius: 8px;
	}

	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		margin-bottom: 8px;
	}

	h2 {
		margin: 0;
		font-size: 15px;
		font-weight: 600;
		overflow-wrap: anywhere;
	}

	/* One column of labels of one width, one row height, one type for every
	   value: the cards are read down a list, and side by side across. */
	dl {
		display: grid;
		grid-template-columns: 88px 1fr;
		row-gap: 4px;
		column-gap: 12px;
		margin: 0 0 8px;
		font-size: 13px;
		line-height: 20px;
	}

	dt {
		color: oklch(0.62 0 0);
	}

	dd {
		margin: 0;
		min-width: 0;
		color: oklch(0.93 0 0);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.err {
		margin: 0 0 12px;
		color: oklch(0.65 0.2 25);
	}

	.hint {
		margin: 0 0 8px;
		color: oklch(0.62 0 0);
	}
</style>
