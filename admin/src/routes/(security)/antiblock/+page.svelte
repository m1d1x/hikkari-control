<script lang="ts">
	import { onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import ReachChart from '$components/entry/ReachChart.svelte';
	import TrendChart from '$components/entry/TrendChart.svelte';
	import {
		fetchOverview,
		describeEntryError,
		formatCents,
		PROTO_LABEL,
		type EntryOverview,
		type OverviewWindow,
		type ProtocolBlock,
	} from '$lib/api/entry';

	// The entry-points console's first screen (roadmap 42 П9.9 §6а). Read-only:
	// nothing here acts, and the pages that act compute no analytics. Counters
	// of inventory — relays alive, domains in the pool — are deliberately
	// absent: with the autopilot running they prompt no decision.

	let data = $state<EntryOverview | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<OverviewWindow>('7d');

	const WINDOWS: OverviewWindow[] = ['24h', '7d', '30d'];

	// A figure the hub cannot compute is named, never defaulted. The screen
	// says "нет данных" and why, instead of something plausible.
	/** Megabytes as the per-person strip says them. */
	function mbLabel(mb: number): string {
		return mb >= 1024 ? `${(mb / 1024).toFixed(2)} ГБ` : `${mb.toFixed(mb < 10 ? 2 : 0)} МБ`;
	}

	const missingOf = $derived(new Map((data?.missing ?? []).map((m) => [m.figure, m.why])));

	async function load() {
		try {
			data = await fetchOverview(window);
			loadError = null;
		} catch (e) {
			loadError = describeEntryError(e);
		}
	}

	function pick(w: OverviewWindow) {
		if (w === window) return;
		window = w;
		void load();
	}

	onMount(load);

	/** A delta worth showing, with the direction said rather than implied. */
	function delta(now: number | null, before: number | null): { text: string; good: boolean } | null {
		if (now === null || before === null || before === 0) return null;
		const d = now - before;
		if (Math.abs(d) < 0.05) return null;
		return { text: `${d > 0 ? '+' : '−'}${Math.abs(d).toFixed(1)}`, good: d > 0 };
	}

	function hours(v: number | null): string {
		if (v === null) return '—';
		return v < 48 ? `${Math.round(v)} ч` : `${(v / 24).toFixed(1)} дн`;
	}

	// The last two measured buckets: how the first-try share moved most
	// recently. Buckets nobody attempted in are skipped, not read as zero.
	const reachTrend = $derived.by(() => {
		const measured = (data?.reach.series ?? []).filter((p) => p.firstPct !== null);
		if (measured.length < 2) return null;
		const d = (measured[measured.length - 1].firstPct as number) - (measured[measured.length - 2].firstPct as number);
		if (Math.abs(d) < 0.1) return null;
		return { text: `${d > 0 ? '+' : '−'}${Math.abs(d).toFixed(1)}`, good: d > 0 };
	});

	/** Days the window spans; a 24h window is one day. */
	const windowDays = $derived(window === '24h' ? 1 : window === '30d' ? 30 : 7);

	/** Days as the screen says them: hours under two days. */
	function life(days: number | null): string {
		if (days === null) return 'нет данных';
		return days < 2 ? `${Math.round(days * 24)} ч` : `${days.toFixed(1)} дн`;
	}

	const C = {
		user: 'oklch(0.75 0.18 155)',
		total: 'oklch(0.92 0.02 90)',
		machines: 'oklch(0.70 0.16 230)',
		domains: 'oklch(0.78 0.16 65)',
		down: 'oklch(0.70 0.16 230)',
		up: 'oklch(0.72 0.18 310)',
	};

	const gb = (v: number) => `${v < 10 ? v.toFixed(2) : v.toFixed(1)} ГБ`;

	function getProtoStyle(proto: string): { color: string; tint: string } {
		if (proto.includes('reality')) {
			return { color: 'var(--c-reality)', tint: 'oklch(0.75 0.14 350 / 0.13)' };
		}
		if (proto.includes('hyst')) {
			return { color: 'var(--c-hyst)', tint: 'oklch(0.72 0.12 200 / 0.13)' };
		}
		if (proto.includes('xhttp')) {
			return { color: 'var(--c-xhttp)', tint: 'oklch(0.78 0.12 85 / 0.13)' };
		}
		if (proto.includes('direct')) {
			return { color: 'var(--c-direct)', tint: 'oklch(0.72 0.15 155 / 0.13)' };
		}
		return { color: 'var(--c-unknown)', tint: 'oklch(0.72 0.16 290 / 0.13)' };
	}
</script>

<PageShell>
	<div class="antiblock-page">
		<div class="head">
			<div>
				<h1>антиблок</h1>
			</div>

			{#if data?.needsYou && data.needsYou.length > 0}
				<a href="/incidents" class="head-alert-pill">
					<span class="pill-dot"></span>
					<span
						><b>{data.needsYou.length}</b>
						{data.needsYou.length === 1 ? 'инцидент требует внимания' : 'инцидента требуют внимания'}</span
					>
					<span class="pill-arrow">&rarr;</span>
				</a>
			{/if}

			<div class="ranges">
				{#each WINDOWS as w (w)}
					<button class="chip" class:on={window === w} onclick={() => pick(w)}>{w}</button>
				{/each}
			</div>
		</div>

		{#if loadError}
			<p class="err" role="alert">{loadError}</p>
		{:else if !data}
			<p class="hint">загружаю…</p>
		{:else}
			<!-- ======== 1. Доходимость ======== -->
			<section class="card reach-card">
				{#if data.reach.attempts === 0}
					<p class="empty">за окно ни одной попытки от клиентов: доходимость считать не из чего</p>
					<ReachChart series={data.reach.series} bucketSeconds={data.bucketSeconds} />
				{:else}
					<div class="reach">
						<div class="figure">
							<div class="big">
								{data.reach.firstTryPct.toFixed(1)}<span class="unit">%</span>
								{#if reachTrend}
									<span class="trend" class:good={reachTrend.good} class:bad={!reachTrend.good}>{reachTrend.text}</span>
								{/if}
							</div>
							<div class="what">с первой попытки</div>
							<div class="breakdown">
								<div>
									<span><i class="sw sw-first"></i>сразу, без перебора</span>
									<b>{data.reach.firstTryPct.toFixed(1)}%</b>
								</div>
								<div>
									<span><i class="sw sw-retried"></i>со 2-го релея и дальше</span>
									<b>{data.reach.retriedPct.toFixed(1)}%</b>
								</div>
								<div>
									<span><i class="sw sw-failed"></i>не дошли</span>
									<b class="bad">{data.reach.failedPct.toFixed(1)}%</b>
								</div>
							</div>
						</div>
						<div class="chart-wrapper">
							<ReachChart series={data.reach.series} bucketSeconds={data.bucketSeconds} />
						</div>
					</div>
				{/if}
			</section>

			<!-- ======== 2. Деньги ======== -->
			{@const runRateMonthCents = data.money.total.perDayCents * 30}
			<section class="card money-card">
				<div class="money-layout">
					<div class="money-figure">
						<div class="big">
							{#if data.money.perUserMonthCents === null}
								<span class="nodata">нет данных</span>
							{:else}
								{formatCents(data.money.perUserMonthCents)}<span class="unit">/ чел · мес</span>
							{/if}
						</div>
						<div class="what">
							<i class="sw sw-user"></i>себестоимость обхода на человека
						</div>

						<div class="breakdown">
							<div>
								<span><i class="sw sw-total"></i>всего сеть</span>
								<b>{formatCents(data.money.total.perDayCents)}<small>/день</small></b>
							</div>
							<div>
								<span><i class="sw sw-machines"></i>серверы</span>
								<b>
									{#if missingOf.has('money.machines')}
										<span class="nodata" title={missingOf.get('money.machines')}>нет цены</span>
									{:else}
										{formatCents(data.money.machines.perDayCents)}<small>/день</small>
									{/if}
								</b>
							</div>
							<div>
								<span><i class="sw sw-domains"></i>домены</span>
								<b>{formatCents(data.money.domains.perDayCents)}<small>/день</small></b>
							</div>
							<div class="total-row">
								<span class="sub-dim">прогноз на месяц</span>
								<b>{formatCents(runRateMonthCents)}</b>
							</div>
							{#if data.money.monthlyCapCents}
								<div>
									<span class="sub-dim">лимит бюджета</span>
									<b>{formatCents(data.money.monthlyCapCents)}</b>
								</div>
							{/if}
							<div>
								<span class="sub-dim">активных людей</span>
								<b>{data.money.activePeople.toLocaleString('ru')}</b>
							</div>
							{#if data.money.machinesUnpriced > 0}
								<div>
									<span class="sub-dim">машин без цены</span>
									<b class="bad">{data.money.machinesUnpriced}</b>
								</div>
							{/if}
						</div>
					</div>

					<div class="chart-wrapper">
						<TrendChart
							buckets={data.money.series.map((p) => p.bucket)}
							bucketSeconds={data.bucketSeconds}
							formatAxis={(v) => formatCents(Math.round(v))}
							empty="за окно не было ни расходов, ни машин с ценой"
							series={[
								{
									key: 'user',
									label: 'на человека, в месяц',
									color: C.user,
									primary: true,
									values: data.money.series.map((p) => p.perUserMonthCents),
									format: (v) => `${formatCents(v)}/мес`,
								},
								{
									key: 'total',
									label: 'всего сеть',
									color: C.total,
									values: data.money.series.map((p) => p.totalCents),
									format: (v) => `${formatCents(v)}/сут`,
								},
								{
									key: 'machines',
									label: 'серверы',
									color: C.machines,
									values: data.money.series.map((p) => p.machinesPerDayCents),
									format: (v) => `${formatCents(v)}/сут`,
								},
								{
									key: 'domains',
									label: 'домены, куплено',
									color: C.domains,
									values: data.money.series.map((p) => p.domainsCents),
									format: (v) => formatCents(v),
								},
							]}
						/>
					</div>
				</div>
			</section>

			<!-- ======== 3. Трафик ======== -->
			<section class="card traffic-card">
				{#if data.traffic === null}
					<TrendChart
						buckets={[]}
						bucketSeconds={data.bucketSeconds}
						formatAxis={gb}
						series={[]}
						empty={missingOf.get('traffic') ?? 'за окно ни один релей не прислал пульс'}
					/>
				{:else}
					{@const perDayGb = data.traffic.gbTotal / windowDays}
					<div class="traffic-layout">
						<div class="traffic-figure">
							<div class="big">
								{#if data.traffic.gbPerPersonDay === null}
									<span class="nodata">нет данных</span>
								{:else}
									{(data.traffic.gbPerPersonDay * 30).toFixed(1)}<span class="unit">ГБ / чел · мес</span>
								{/if}
							</div>
							<div class="what">
								<i class="sw sw-user"></i>расход трафика на человека
							</div>

							<div class="breakdown">
								<div>
									<span><i class="sw sw-total"></i>всего сеть</span>
									<b>{perDayGb.toFixed(2)}<small>ГБ/день</small></b>
								</div>
								<div>
									<span><i class="sw sw-down"></i>к людям (down)</span>
									<b>{(data.traffic.gbDown / windowDays).toFixed(2)}<small>ГБ/день</small></b>
								</div>
								<div>
									<span><i class="sw sw-up"></i>от людей (up)</span>
									<b>{(data.traffic.gbUp / windowDays).toFixed(2)}<small>ГБ/день</small></b>
								</div>
								<div class="total-row">
									<span class="sub-dim">прогноз на месяц</span>
									<b>{(perDayGb * 30).toFixed(0)} <small>ГБ</small></b>
								</div>
								<div>
									<span class="sub-dim">релеев прислали пульс</span>
									<b>{data.traffic.relaysReporting}</b>
								</div>
							</div>
						</div>

						<div class="chart-wrapper">
							<TrendChart
								buckets={data.traffic.series.map((p) => p.bucket)}
								bucketSeconds={data.bucketSeconds}
								formatAxis={gb}
								empty="релеи пульсуют, но за окно не набралось двух пульсов подряд"
								series={[
									{
										key: 'down',
										label: 'к людям (down)',
										color: C.down,
										primary: true,
										values: data.traffic.series.map((p) => p.gbDown),
										format: gb,
									},
									{
										key: 'up',
										label: 'от людей (up)',
										color: C.up,
										values: data.traffic.series.map((p) => p.gbUp),
										format: gb,
									},
								]}
							/>
						</div>
					</div>
				{/if}

				<!-- Per person, read per account: the message socket only. -->
				<div class="per-user" title={missingOf.get('traffic.perUser.media') ?? ''}>
					<span class="pu-title">на человека · сокет сообщений</span>
					{#if data.trafficPerUser === null}
						<span class="nodata" title={missingOf.get('traffic.perUser') ?? ''}>нет данных</span>
					{:else}
						{@const pu = data.trafficPerUser}
						<span class="pu-cell">медиана <b>{mbLabel(pu.medianMbDay)}</b><small>/день</small></span>
						<span class="pu-cell">p90 <b>{mbLabel(pu.p90MbDay)}</b><small>/день</small></span>
						<span class="pu-cell">
							обход, медиана
							{#if pu.viaRelayMedianMbDay === null}
								<span class="nodata">никто не шёл через релей</span>
							{:else}
								<b>{mbLabel(pu.viaRelayMedianMbDay)}</b><small>/день</small>
							{/if}
						</span>
						<span class="pu-cell">через релеи <b>{pu.viaRelayPct.toFixed(1)}%</b><small>байтов</small></span>
						<span class="pu-cell">людей <b>{pu.people}</b><small>из них через релей {pu.viaRelayPeople}</small></span>
					{/if}
				</div>
			</section>

			<!-- ======== 4. Жизнь ======== -->
			{@const domainDays = data.survival.domainMedianHours === null ? null : data.survival.domainMedianHours / 24}
			<section class="card survival-card">
				<div class="survival-layout">
					<div class="survival-figure">
						<div class="big">
							{#if data.survival.boxMedianDays === null}
								<span class="nodata">нет данных</span>
							{:else}
								{life(data.survival.boxMedianDays)}
							{/if}
						</div>
						<div class="what">
							<i class="sw sw-box"></i>медиана жизни машины до бана
						</div>

						<div class="breakdown">
							<div>
								<span><i class="sw sw-relay"></i>релеи до блокировки</span>
								<b>{life(data.survival.relayMedianDays)}</b>
							</div>
							<div>
								<span><i class="sw sw-domain"></i>домены до блокировки</span>
								<b>{life(domainDays)}</b>
							</div>
							<div class="total-row">
								<span class="sub-dim">сгорело релеев за окно</span>
								<b class:bad={data.survival.censorDeaths > 0}>{data.survival.censorDeaths}</b>
							</div>
							<div>
								<span class="sub-dim">релеев за всё время</span>
								<b>{data.survival.relaysEver.toLocaleString('ru')}</b>
							</div>
						</div>
					</div>

					<div class="chart-wrapper">
						<TrendChart
							buckets={data.survival.series.map((p) => p.bucket)}
							bucketSeconds={data.bucketSeconds}
							formatAxis={(v) => life(v)}
							empty="за окно цензор ничего не сжёг"
							series={[
								{
									key: 'box',
									label: 'машины',
									color: C.machines,
									primary: true,
									values: data.survival.series.map((p) => p.boxMedianDays),
									format: (v) => life(v),
								},
								{
									key: 'relay',
									label: 'релеи',
									color: C.up,
									values: data.survival.series.map((p) => p.relayMedianDays),
									format: (v) => life(v),
								},
								{
									key: 'domain',
									label: 'домены',
									color: C.domains,
									values: data.survival.series.map((p) => p.domainMedianDays),
									format: (v) => life(v),
								},
							]}
						/>
					</div>
				</div>
			</section>

			<!-- ======== 5. Протоколы (Full-Width Card) ======== -->
			<section class="card protos-card">
				<div class="protos-header">
					<h2>протоколы</h2>
				</div>

				{#if data.protocols.length === 0}
					<p class="empty">ни одной успешной попытки за окно</p>
				{:else}
					<!-- Общая 100% полоска соотношений (Stacked Ratio Bar) -->
					<div class="ratio-section">
						<div class="ratio-bar">
							{#each data.protocols as p (p.proto)}
								{@const st = getProtoStyle(p.proto)}
								<div
									class="ratio-seg"
									style="width: {p.sharePct}%; background: {st.color};"
									title="{PROTO_LABEL[p.proto] ?? p.proto}: {p.sharePct.toFixed(1)}% ({p.attempts.toLocaleString(
										'ru',
									)} попыток)"
								></div>
							{/each}
						</div>

						<div class="ratio-legend">
							{#each data.protocols as p (p.proto)}
								{@const st = getProtoStyle(p.proto)}
								<div class="legend-chip" title="{p.attempts.toLocaleString('ru')} попыток">
									<span class="chip-dot" style="background: {st.color}"></span>
									<span class="chip-name">{PROTO_LABEL[p.proto] ?? p.proto}</span>
									<b class="chip-pct" style="color: {st.color}">{p.sharePct.toFixed(0)}%</b>
								</div>
							{/each}
						</div>
					</div>

					<!-- Сетка карточек по каждому протоколу -->
					<div class="protos-grid">
						{#each data.protocols as p (p.proto)}
							{@const st = getProtoStyle(p.proto)}
							<div class="proto-card" title="{p.attempts.toLocaleString('ru')} попыток за окно">
								<!-- Шапка карточки: название и доля -->
								<div class="card-top">
									<div class="proto-badge">
										<span class="proto-dot" style="background: {st.color}"></span>
										<span class="proto-title">{PROTO_LABEL[p.proto] ?? p.proto}</span>
									</div>
									<div class="proto-share" style="color: {st.color}">
										{p.sharePct.toFixed(0)}<span class="share-unit">%</span>
									</div>
								</div>

								<!-- Микро-индикатор доли -->
								<div class="card-bar-track">
									<div class="card-bar-fill" style="width: {p.sharePct}%; background: {st.color}"></div>
								</div>

								<!-- 4 ключевые метрики связи (ровные колонки без переносов) -->
								<div class="proto-metrics">
									<div class="m-cell">
										<div class="m-val">
											{p.p50Ms > 0 ? p.p50Ms.toFixed(0) : '—'}<small>мс</small>
										</div>
										<div class="m-lbl">пинг p50</div>
									</div>
									<div class="m-cell">
										<div class="m-val">
											{p.p95Ms > 0 ? p.p95Ms.toFixed(0) : '—'}<small>мс</small>
										</div>
										<div class="m-lbl">пинг p95</div>
									</div>
									<div class="m-cell">
										<div class="m-val">
											{(p.connectP50Ms / 1000).toFixed(1)}<small>с</small>
										</div>
										<div class="m-lbl">подключение</div>
									</div>
									<div class="m-cell">
										<div class="m-val muted">—</div>
										<div class="m-lbl">потери</div>
									</div>
								</div>
							</div>
						{/each}
					</div>
				{/if}
			</section>
		{/if}
	</div>
</PageShell>

<style>
	.antiblock-page {
		--bg: oklch(0.14 0 0);
		--bg-raised: oklch(0.17 0 0);
		--surface: oklch(0.23 0 0);
		--surface-hover: oklch(0.26 0 0);
		--card-bg: oklch(0.2 0 0);
		--inset: oklch(0.17 0 0);

		--primary: oklch(0.75 0.14 350);
		--primary-subtle: oklch(0.75 0.14 350 / 0.1);

		--ok: oklch(0.72 0.15 155);
		--warn: oklch(0.78 0.12 85);
		--bad: oklch(0.65 0.2 25);
		--unknown: oklch(0.52 0 0);

		--c-reality: oklch(0.75 0.14 350);
		--c-hyst: oklch(0.72 0.12 200);
		--c-xhttp: oklch(0.78 0.12 85);
		--c-direct: oklch(0.72 0.15 155);
		--c-unknown: oklch(0.72 0.16 290);

		--border: oklch(0.24 0 0);
		--border-subtle: oklch(0.19 0 0);

		--text: oklch(0.93 0 0);
		--text-2: oklch(0.62 0 0);
		--text-3: oklch(0.42 0 0);

		--sans: 'Inter', -apple-system, BlinkMacSystemFont, sans-serif;
		--mono: 'IBM Plex Mono', monospace;

		color: var(--text);
		font-family: var(--sans);
		font-size: 13px;
		line-height: 1.5;
		-webkit-font-smoothing: antialiased;
	}

	.hint,
	.empty {
		margin: 0;
		padding: 16px;
		font-size: 13px;
		color: var(--text-2);
	}

	/* A figure the hub cannot compute: said, not drawn as a plausible number. */
	.nodata {
		font-size: 15px;
		font-weight: 500;
		color: var(--text-3);
		letter-spacing: 0;
	}

	.err {
		margin: 0;
		color: var(--bad);
		font-size: 13px;
	}

	/* ---------- head ---------- */

	.head {
		display: flex;
		align-items: center;
		gap: 16px;
		margin-bottom: 18px;
	}

	.head h1 {
		font-size: 20px;
		font-weight: 600;
		letter-spacing: -0.01em;
		color: var(--text);
		margin: 0;
	}

	.ranges {
		margin-left: auto;
		display: flex;
		gap: 4px;
	}

	.chip {
		padding: 4px 11px;
		border-radius: 6px;
		font-size: 11px;
		color: var(--text-2);
		border: 1px solid var(--border);
		white-space: nowrap;
		cursor: pointer;
		background: none;
		transition: background 120ms ease;
	}

	.chip:hover {
		background: var(--surface);
	}

	.chip.on {
		color: var(--text);
		background: var(--primary-subtle);
		border-color: oklch(0.75 0.14 350 / 0.35);
	}

	/* ---------- cards ---------- */

	.card {
		background: var(--card-bg);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		margin-bottom: 16px;
	}

	.card-head {
		display: flex;
		align-items: baseline;
		gap: 10px;
		padding: 13px 16px;
		border-bottom: 1px solid var(--border-subtle);
	}

	.card-head h2 {
		margin: 0;
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
	}

	.card-head .right {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 11px;
		color: var(--text-3);
	}

	.card-body {
		padding: 16px;
	}

	/* ---------- reach ---------- */

	.reach-card {
		padding: 18px 20px;
	}

	.reach {
		display: grid;
		grid-template-columns: 240px 1fr;
		gap: 24px;
		align-items: center;
	}

	.figure .big {
		display: flex;
		align-items: baseline;
		gap: 8px;
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-size: 44px;
		font-weight: 600;
		letter-spacing: -0.03em;
		line-height: 1;
		color: var(--text);
	}

	.figure .unit {
		font-size: 18px;
		font-weight: 400;
		color: var(--text-3);
	}

	.figure .what {
		margin: 8px 0 0;
		font-size: 12px;
		color: var(--text-2);
	}

	.breakdown {
		margin-top: 14px;
		padding-top: 12px;
		border-top: 1px solid var(--border-subtle);
		display: flex;
		flex-direction: column;
		gap: 5px;
	}

	.breakdown div {
		display: flex;
		justify-content: space-between;
		gap: 10px;
		font-size: 11px;
		color: var(--text-3);
	}

	.breakdown div span {
		display: flex;
		align-items: center;
		gap: 7px;
	}

	.sw {
		width: 8px;
		height: 8px;
		border-radius: 2px;
		flex-shrink: 0;
	}

	.sw-first {
		background: oklch(0.72 0.15 155 / 0.55);
	}

	.sw-retried {
		background: oklch(0.78 0.12 85 / 0.6);
	}

	.sw-failed {
		background: oklch(0.65 0.2 25 / 0.85);
	}

	.breakdown div b {
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-weight: 500;
		color: var(--text-2);
	}

	.breakdown div b.bad {
		color: var(--bad);
	}

	.chart-wrapper {
		min-width: 0;
	}

	/* ---------- money card ---------- */

	.money-card {
		padding: 18px 20px;
	}

	.money-layout {
		display: grid;
		grid-template-columns: 240px 1fr;
		gap: 24px;
		align-items: center;
	}

	.money-figure {
		display: flex;
		flex-direction: column;
		justify-content: center;
	}

	.money-figure .big {
		display: flex;
		align-items: baseline;
		gap: 6px;
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-size: 42px;
		font-weight: 600;
		letter-spacing: -0.03em;
		line-height: 1;
		color: var(--text);
	}

	.money-figure .unit {
		font-size: 16px;
		font-weight: 400;
		color: var(--text-3);
	}

	.money-figure .what {
		display: flex;
		align-items: center;
		gap: 6px;
		margin: 8px 0 0;
		font-size: 12px;
		color: var(--text-2);
	}

	.sw-user {
		background: oklch(0.75 0.18 155);
	}

	.sw-machines {
		background: oklch(0.7 0.16 230);
	}

	.sw-domains {
		background: oklch(0.78 0.16 65);
	}

	.sw-total {
		background: oklch(0.92 0.02 90);
	}

	.breakdown .total-row {
		padding-top: 6px;
		border-top: 1px dashed var(--border-subtle);
		margin-top: 2px;
	}

	.breakdown .sub-dim {
		color: var(--text-3);
	}

	.breakdown div b small {
		font-size: 9px;
		font-weight: 400;
		color: var(--text-3);
		margin-left: 2px;
	}

	/* ---------- per person strip ---------- */
	.per-user {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 6px 22px;
		padding: 10px 18px 12px;
		border-top: 1px solid var(--border-subtle);
		font-size: 12px;
		color: var(--text-2);
	}

	.per-user .pu-title {
		font-size: 11px;
		color: var(--text-3);
	}

	.per-user b {
		margin-left: 4px;
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		color: var(--text);
		font-weight: 600;
	}

	.per-user small {
		margin-left: 3px;
		color: var(--text-3);
	}

	/* ---------- traffic card ---------- */

	.traffic-card {
		padding: 18px 20px;
	}

	.traffic-layout {
		display: grid;
		grid-template-columns: 240px 1fr;
		gap: 24px;
		align-items: center;
	}

	.traffic-figure {
		display: flex;
		flex-direction: column;
		justify-content: center;
	}

	.traffic-figure .big {
		display: flex;
		align-items: baseline;
		gap: 6px;
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-size: 40px;
		font-weight: 600;
		letter-spacing: -0.03em;
		line-height: 1;
		color: var(--text);
	}

	.traffic-figure .unit {
		font-size: 15px;
		font-weight: 400;
		color: var(--text-3);
	}

	.traffic-figure .what {
		display: flex;
		align-items: center;
		gap: 6px;
		margin: 8px 0 0;
		font-size: 12px;
		color: var(--text-2);
	}

	.sw-down {
		background: oklch(0.7 0.16 230);
	}

	.sw-up {
		background: oklch(0.72 0.18 310);
	}

	/* ---------- survival card ---------- */

	.survival-card {
		padding: 18px 20px;
		margin-bottom: 16px;
	}

	.survival-layout {
		display: grid;
		grid-template-columns: 240px 1fr;
		gap: 24px;
		align-items: center;
	}

	.survival-figure {
		display: flex;
		flex-direction: column;
		justify-content: center;
	}

	.survival-figure .big {
		display: flex;
		align-items: baseline;
		gap: 6px;
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-size: 40px;
		font-weight: 600;
		letter-spacing: -0.03em;
		line-height: 1;
		color: var(--text);
	}

	.survival-figure .what {
		display: flex;
		align-items: center;
		gap: 6px;
		margin: 8px 0 0;
		font-size: 12px;
		color: var(--text-2);
	}

	.sw-box {
		background: oklch(0.7 0.16 230);
	}

	.sw-relay {
		background: oklch(0.72 0.18 310);
	}

	.sw-domain {
		background: oklch(0.78 0.16 65);
	}

	.trend {
		font-family: var(--mono);
		font-size: 13px;
		font-weight: 500;
	}

	.trend.good {
		color: var(--ok);
	}

	.trend.bad {
		color: var(--bad);
	}

	/* ---------- header incident alert pill ---------- */

	.head-alert-pill {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 4px 12px;
		background: oklch(0.2 0.08 25);
		border: 1px solid oklch(0.48 0.18 25 / 0.5);
		border-radius: 20px;
		font-size: 11px;
		color: var(--text-2);
		text-decoration: none;
		white-space: nowrap;
		transition:
			background 120ms ease,
			border-color 120ms ease;
	}

	.head-alert-pill:hover {
		background: oklch(0.25 0.1 25);
		border-color: oklch(0.58 0.2 25);
		color: var(--text);
	}

	.head-alert-pill .pill-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--bad);
		box-shadow: 0 0 6px var(--bad);
		flex-shrink: 0;
	}

	.head-alert-pill b {
		color: var(--bad);
		font-weight: 600;
	}

	.head-alert-pill .pill-arrow {
		color: var(--text-3);
		font-family: var(--mono);
		font-size: 12px;
	}

	.head-alert-pill:hover .pill-arrow {
		color: var(--text);
	}

	/* ---------- protocols (full-width card) ---------- */

	.protos-card {
		padding: 18px 20px;
		margin-bottom: 16px;
	}

	.protos-header {
		margin-bottom: 14px;
	}

	.protos-header h2 {
		margin: 0;
		font-size: 15px;
		font-weight: 600;
		color: var(--text);
	}

	/* 100% stacked distribution bar */
	.ratio-section {
		margin-bottom: 16px;
		padding: 12px 14px;
		background: var(--inset);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
	}

	.ratio-bar {
		display: flex;
		height: 9px;
		border-radius: 5px;
		overflow: hidden;
		background: oklch(0.22 0 0);
		gap: 2px;
	}

	.ratio-seg {
		height: 100%;
		transition: width 0.25s ease-out;
		min-width: 4px;
	}

	.ratio-legend {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 20px;
		margin-top: 10px;
		font-size: 11px;
	}

	.legend-chip {
		display: flex;
		align-items: center;
		gap: 6px;
		white-space: nowrap;
		cursor: default;
	}

	.chip-dot {
		width: 8px;
		height: 8px;
		border-radius: 2px;
		flex-shrink: 0;
	}

	.chip-name {
		font-family: var(--mono);
		font-weight: 500;
		color: var(--text-2);
	}

	.chip-pct {
		font-family: var(--mono);
		font-size: 12px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
	}

	/* Grid of individual protocol cards */
	.protos-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
		gap: 14px;
	}

	.proto-card {
		background: var(--inset);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		padding: 14px 16px;
		display: flex;
		flex-direction: column;
		transition: border-color 0.15s ease;
	}

	.proto-card:hover {
		border-color: var(--border);
	}

	.card-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
	}

	.proto-badge {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.proto-dot {
		width: 8px;
		height: 8px;
		border-radius: 2px;
		flex-shrink: 0;
	}

	.proto-title {
		font-family: var(--mono);
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
		white-space: nowrap;
	}

	.proto-share {
		font-family: var(--mono);
		font-size: 24px;
		font-weight: 700;
		line-height: 1;
		letter-spacing: -0.02em;
		font-variant-numeric: tabular-nums;
	}

	.share-unit {
		font-size: 14px;
		font-weight: 400;
		opacity: 0.8;
		margin-left: 1px;
	}

	.card-bar-track {
		height: 3px;
		border-radius: 2px;
		background: oklch(0.24 0 0);
		margin: 10px 0 14px;
		overflow: hidden;
	}

	.card-bar-fill {
		height: 100%;
		border-radius: 2px;
	}

	.proto-metrics {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 8px;
	}

	.m-cell {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.m-val {
		font-family: var(--mono);
		font-size: 15px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		color: var(--text);
		display: flex;
		align-items: baseline;
		gap: 2px;
		white-space: nowrap;
	}

	.m-val small {
		font-size: 10px;
		font-weight: 400;
		color: var(--text-3);
	}

	.m-val.muted {
		color: var(--text-3);
	}

	.m-lbl {
		font-size: 10px;
		color: var(--text-3);
		margin-top: 3px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	@media (max-width: 1100px) {
		.reach,
		.money-layout,
		.traffic-layout,
		.survival-layout,
		.protos-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
