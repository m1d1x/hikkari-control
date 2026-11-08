<script lang="ts">
	import WinChips from '$components/product/WinChips.svelte';
	import TrendChart from '$components/entry/TrendChart.svelte';
	import { fetchProductMetrics, type MetricsShare } from '$lib/api/product';
	import { num, pct, secs } from '$lib/product/format';
	import { Windowed } from '$lib/product/windowed.svelte';

	const m = new Windowed('7d', fetchProductMetrics);
	const d = $derived(m.data);

	const OS_COLOR: Record<string, string> = {
		windows: '#6192d4',
		macos: '#ededed',
		linux: '#e8a838',
		android: '#45b97c',
		ios: '#b07cd8',
	};
	const VERSION_COLORS = ['#e8457c', '#6192d4', '#b07cd8', '#45b97c', '#e8a838'];

	const or = (v: number | null, f: (v: number) => string) => (v === null ? '-' : f(v));
	const osColor = (s: MetricsShare) => OS_COLOR[s.key] ?? '#787878';
	const versionColor = (i: number) => VERSION_COLORS[i % VERSION_COLORS.length] ?? '#787878';

	const C = {
		green: 'oklch(0.75 0.18 155)',
		blue: 'oklch(0.70 0.16 230)',
		violet: 'oklch(0.72 0.18 310)',
		orange: 'oklch(0.78 0.16 65)',
	};

	const count = (v: number) => String(Math.round(v));
	const percent = (v: number) => `${v.toFixed(1)}%`;

	/** A daily/weekly/monthly series as the chart wants it: ISO buckets and one value each. */
	const split = (ts?: { data: { timestamp: string; value: number }[] }) => ({
		buckets: (ts?.data ?? []).map((pt) => pt.timestamp),
		values: (ts?.data ?? []).map((pt) => pt.value),
	});
	const dau = $derived(split(d?.people?.dauTimeSeries));
	const wau = $derived(split(d?.people?.wauTimeSeries));
	const mau = $derived(split(d?.people?.mauTimeSeries));
	const stick = $derived(split(d?.people?.stickinessTimeSeries));

	/** The window's own buckets, for the downloads and installs chart. */
	const winBuckets = $derived((d?.buckets ?? []).map((t) => new Date(t).toISOString()));
</script>

<div class="head">
	<div class="head-info">
		<h1>Метрики продукта</h1>
	</div>
	<div class="head-controls">
		{#if m.loading && d}<span class="dim loading-text">обновление…</span>{/if}
		<WinChips bind:value={m.win} />
	</div>
</div>

{#if m.error}
	<div class="error-banner">Ошибка загрузки: {m.error}</div>
{/if}

{#if d}
	<!-- 1. Секция аудитории (DAU / WAU / MAU / Stickiness) с графиками истории -->
	<section class="section-block">
		<div class="section-header">
			<h2 class="section-title">Аудитория</h2>
		</div>
		<div class="audience-grid">
			<div class="chart-card">
				<div class="card-header">
					<div>
						<h3 class="card-title">DAU</h3>
					</div>
					<span class="card-value mono">{num(d.people.dau)}</span>
				</div>
				<TrendChart
					buckets={dau.buckets}
					bucketSeconds={86400}
					formatAxis={count}
					height={135}
					series={[{ key: 'dau', label: 'DAU', color: C.green, primary: true, values: dau.values, format: count }]}
				/>
			</div>

			<div class="chart-card">
				<div class="card-header">
					<div>
						<h3 class="card-title">WAU</h3>
					</div>
					<span class="card-value mono">{num(d.people.wau)}</span>
				</div>
				<TrendChart
					buckets={wau.buckets}
					bucketSeconds={604800}
					formatAxis={count}
					height={135}
					series={[{ key: 'wau', label: 'WAU', color: C.blue, primary: true, values: wau.values, format: count }]}
				/>
			</div>

			<div class="chart-card">
				<div class="card-header">
					<div>
						<h3 class="card-title">MAU</h3>
					</div>
					<span class="card-value mono">{num(d.people.mau)}</span>
				</div>
				<TrendChart
					buckets={mau.buckets}
					bucketSeconds={2592000}
					formatAxis={count}
					height={135}
					series={[{ key: 'mau', label: 'MAU', color: C.violet, primary: true, values: mau.values, format: count }]}
				/>
			</div>

			<div class="chart-card">
				<div class="card-header">
					<div>
						<h3 class="card-title">Липкость</h3>
					</div>
					<span class="card-value mono">{or(d.people.stickiness, pct)}</span>
				</div>
				<TrendChart
					buckets={stick.buckets}
					bucketSeconds={86400}
					formatAxis={percent}
					height={135}
					series={[{ key: 'stick', label: 'Липкость', color: C.orange, primary: true, values: stick.values, format: percent }]}
				/>
			</div>
		</div>
	</section>

	<!-- 1б. Онлайн и приток: малые карточки того же вида, что DAU -->
	<div class="mini-grid">
		<div class="chart-card">
			<div class="card-header">
				<h3 class="card-title">Онлайн</h3>
				<span class="card-value mono">{or(d.online.now, num)}</span>
			</div>
			<TrendChart
				buckets={winBuckets}
				bucketSeconds={d.bucketSeconds}
				formatAxis={count}
				height={135}
				empty="история копится после обновления hub"
				series={[{ key: 'online', label: 'Онлайн', color: C.green, primary: true, values: d.online.series, format: count }]}
			/>
		</div>

		<div class="chart-card">
			<div class="card-header">
				<h3 class="card-title">Запускали клиент</h3>
				<span class="card-value mono">{num(d.people.active)}</span>
			</div>
			<TrendChart
				buckets={winBuckets}
				bucketSeconds={d.bucketSeconds}
				formatAxis={count}
				height={135}
				series={[{ key: 'active', label: 'Запускали клиент', color: C.blue, primary: true, values: d.people.series.active, format: count }]}
			/>
		</div>

		<div class="chart-card">
			<div class="card-header">
				<h3 class="card-title">Новые установки</h3>
				<span class="card-value mono">{num(d.people.fresh)}</span>
			</div>
			<TrendChart
				buckets={winBuckets}
				bucketSeconds={d.bucketSeconds}
				formatAxis={count}
				height={135}
				series={[{ key: 'fresh', label: 'Новые установки', color: C.violet, primary: true, values: d.people.series.fresh, format: count }]}
			/>
		</div>
	</div>

	<!-- 2. Установки: воронка, активность и здоровье соединений в одном месте -->
	<section class="card content-card">
		<div class="section-header">
			<h2 class="section-title">Установки</h2>
		</div>

		<div class="kpi-grid">
			<div class="kpi-box">
				<span class="kpi-label"><i class="ldot" style="background:oklch(0.70 0.16 230)"></i>Кликов «Скачать»</span>
				<div class="kpi-main"><span class="kpi-num mono">{or(d.downloads.clicks, num)}</span></div>
			</div>
			<div class="kpi-box">
				<span class="kpi-label">Установили</span>
				<div class="kpi-main">
					<span class="kpi-num mono">{num(d.downloads.installs)}</span>
					<span class="kpi-side mono">{or(d.downloads.convPct, pct)}</span>
				</div>
			</div>
			<div class="kpi-box">
				<span class="kpi-label">Подняли туннель</span>
				<div class="kpi-main">
					<span class="kpi-num mono">{num(d.activation.connected)}<span class="kpi-of"> из {num(d.activation.installs)}</span></span>
					<span class="kpi-side mono">{or(d.activation.pct, pct)}</span>
				</div>
			</div>
		</div>

		<div class="chart-wrapper">
			<TrendChart
				buckets={winBuckets}
				bucketSeconds={d.bucketSeconds}
				formatAxis={count}
				series={[
					...(d.downloads.clicksSeries
						? [
								{
									key: 'clicks',
									label: 'Клики «Скачать»',
									color: C.blue,
									primary: true,
									values: d.downloads.clicksSeries,
									format: count,
								},
							]
						: []),
				]}
			/>
		</div>
	</section>

	<!-- 4. Секции платформ и версий клиента -->
	<div class="grid-2">
		<section class="card content-card">
			<div class="section-header">
				<div>
					<h2 class="section-title">Операционные системы</h2>
				</div>
			</div>
			{#if d.platforms.length === 0}
				<p class="empty-text">Нет данных за выбранное окно</p>
			{:else}
				<div class="bar">
					{#each d.platforms as p (p.key)}
						<i style="width: {p.pct}%; background: {osColor(p)}"></i>
					{/each}
				</div>
				<div class="shares-table">
					{#each d.platforms as p (p.key)}
						<div class="share-row">
							<div class="share-label">
								<i class="dot" style="background: {osColor(p)}"></i>
								<span>{p.key}</span>
							</div>
							<div class="share-values">
								<b class="mono">{num(p.installs)}</b>
								<span class="share-pct dim mono">{p.pct.toFixed(1)}%</span>
							</div>
						</div>
					{/each}
				</div>
			{/if}
		</section>

		<section class="card content-card">
			<div class="section-header">
				<div>
					<h2 class="section-title">Версии клиента</h2>
				</div>
			</div>
			{#if d.versions.length === 0}
				<p class="empty-text">Нет данных за выбранное окно</p>
			{:else}
				<div class="bar">
					{#each d.versions as v, i (v.key)}
						<i style="width: {v.pct}%; background: {versionColor(i)}"></i>
					{/each}
				</div>
				<div class="shares-table">
					{#each d.versions as v, i (v.key)}
						<div class="share-row">
							<div class="share-label">
								<i class="dot" style="background: {versionColor(i)}"></i>
								<span>v{v.key}</span>
							</div>
							<div class="share-values">
								<b class="mono">{num(v.installs)}</b>
								<span class="share-pct dim mono">{v.pct.toFixed(1)}%</span>
							</div>
						</div>
					{/each}
				</div>
			{/if}
		</section>
	</div>
{:else if m.loading}
	<div class="card content-card center-card">
		<p class="loading-state">Загрузка продуктовых метрик…</p>
	</div>
{/if}

<style>
	.mini-grid {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 16px;
		margin-bottom: 24px;
	}

	/* Header */
	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 16px;
		margin-bottom: 24px;
	}

	.head-info h1 {
		font-size: 22px;
		font-weight: 700;
		letter-spacing: -0.02em;
		color: var(--text);
		margin: 0;
	}

	.subtitle {
		display: block;
		font-size: 13px;
		color: var(--text-2);
		margin-top: 4px;
	}

	.head-controls {
		display: flex;
		align-items: center;
		gap: 12px;
	}

	.loading-text {
		font-size: 12px;
	}

	.error-banner {
		padding: 12px 16px;
		margin-bottom: 20px;
		border-radius: 8px;
		background: rgba(220, 38, 38, 0.15);
		border: 1px solid rgba(220, 38, 38, 0.3);
		color: #f87171;
		font-size: 13px;
	}

	/* Section Blocks & Cards */
	.section-block {
		margin-bottom: 24px;
	}

	.content-card {
		background: var(--card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 20px 24px;
		margin-bottom: 24px;
	}

	.section-header {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		margin-bottom: 16px;
	}

	.section-title {
		font-size: 15px;
		font-weight: 600;
		color: var(--text);
		margin: 0;
	}

	.section-desc {
		font-size: 12px;
		color: var(--text-3);
		margin-left: 10px;
	}

	/* 1. Audience 4-Card Grid */
	.audience-grid {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 16px;
	}

	.chart-card {
		background: var(--card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 16px;
		display: flex;
		flex-direction: column;
	}

	.card-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		margin-bottom: 8px;
	}

	.card-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
		margin: 0;
	}

	.card-hint {
		font-size: 11px;
		color: var(--text-3);
		display: block;
		margin-top: 2px;
	}

	.card-value {
		font-size: 22px;
		font-weight: 600;
		color: var(--text);
		line-height: 1;
	}

	/* 2. KPI Mini Grid */
	.kpi-grid {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 14px;
		margin-bottom: 20px;
	}

	.kpi-box {
		background: rgba(255, 255, 255, 0.02);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		padding: 12px 14px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.kpi-label {
		font-size: 11px;
		font-weight: 500;
		color: var(--text-3);
	}

	.ldot {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		margin-right: 6px;
	}

	.kpi-side {
		font-size: 13px;
		color: var(--text-3);
	}

	.kpi-of {
		font-size: 14px;
		font-weight: 400;
		color: var(--text-3);
	}

	.kpi-main {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin: 2px 0;
	}

	.kpi-num {
		font-size: 24px;
		font-weight: 600;
		color: var(--text);
		line-height: 1;
	}

	.text-ok {
		color: #45b97c !important;
	}

	.kpi-tag {
		font-size: 10px;
		padding: 2px 6px;
		border-radius: 4px;
		font-weight: 500;
	}

	.active-tag {
		background: rgba(232, 69, 124, 0.15);
		color: #e8457c;
	}

	.new-tag {
		background: rgba(69, 185, 124, 0.15);
		color: #45b97c;
	}

	.returning-tag {
		background: rgba(97, 146, 212, 0.15);
		color: #6192d4;
	}

	.total-tag {
		background: rgba(255, 255, 255, 0.08);
		color: var(--text-2);
	}

	.kpi-sub {
		font-size: 11px;
		color: var(--text-3);
	}

	.chart-wrapper {
		margin: 8px 0 16px;
	}

	.mt-16 {
		margin-top: 16px;
	}

	/* Health Strip */
	.health-strip {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 16px;
		margin-top: 16px;
		padding-top: 16px;
		border-top: 1px solid var(--border-subtle);
	}

	.health-box {
		display: flex;
		flex-direction: column;
		gap: 3px;
	}

	.h-label {
		font-size: 11px;
		color: var(--text-3);
	}

	.h-val {
		font-size: 17px;
		font-weight: 600;
		color: var(--text);
		letter-spacing: -0.01em;
	}

	.h-sub {
		font-size: 11px;
		color: var(--text-3);
	}

	/* 3. Funnel */
	.funnel-container {
		display: grid;
		grid-template-columns: 1fr auto 1fr auto 1fr;
		align-items: stretch;
		gap: 14px;
		margin-bottom: 8px;
	}

	.funnel-step {
		background: rgba(255, 255, 255, 0.02);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		padding: 16px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.step-header {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.step-badge {
		font-size: 10px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		padding: 2px 6px;
		border-radius: 4px;
		background: var(--surface);
		color: var(--text-2);
	}

	.step-name {
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
	}

	.step-num {
		font-size: 26px;
		font-weight: 600;
		color: var(--text);
		line-height: 1.1;
		margin: 4px 0 2px;
	}

	.step-sub-num {
		font-size: 13px;
		color: var(--text-3);
		font-weight: 400;
	}

	.step-desc {
		font-size: 11px;
		color: var(--text-3);
	}

	.step-alert {
		margin-top: 8px;
		font-size: 11px;
		padding: 4px 8px;
		border-radius: 4px;
		align-self: flex-start;
	}

	.step-alert.warning {
		background: rgba(232, 168, 56, 0.12);
		color: #e8a838;
		border: 1px solid rgba(232, 168, 56, 0.25);
	}

	.step-alert.success {
		background: rgba(69, 185, 124, 0.12);
		color: #45b97c;
		border: 1px solid rgba(69, 185, 124, 0.25);
	}

	.step-kpi {
		margin-top: 8px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.kpi-pill {
		font-size: 12px;
		color: var(--text);
	}

	.kpi-pill b {
		color: var(--text);
	}

	.time-sub {
		font-size: 11px;
		color: var(--text-3);
	}

	.funnel-divider {
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.arrow-icon {
		font-size: 20px;
		color: var(--text-3);
		opacity: 0.6;
	}

	/* Breakdown and Platforms */
	.breakdown-section {
		margin-top: 20px;
		padding-top: 16px;
		border-top: 1px solid var(--border-subtle);
	}

	.breakdown-title {
		font-size: 12px;
		font-weight: 500;
		color: var(--text-2);
		margin-bottom: 8px;
		display: block;
	}

	.bar {
		display: flex;
		gap: 2px;
		height: 6px;
		border-radius: 3px;
		overflow: hidden;
		margin: 8px 0 10px;
	}

	.bar i {
		opacity: 0.9;
		transition: width 200ms ease;
	}

	.chips-list {
		display: flex;
		flex-wrap: wrap;
		gap: 12px;
	}

	.chip-item {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		color: var(--text);
	}

	.dot {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	/* 4. Grid 2 */
	.grid-2 {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 16px;
	}

	.shares-table {
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin-top: 12px;
	}

	.share-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		font-size: 12px;
		padding: 4px 0;
	}

	.share-label {
		display: flex;
		align-items: center;
		gap: 8px;
		color: var(--text);
	}

	.share-values {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.share-pct {
		font-size: 11px;
		width: 48px;
		text-align: right;
	}

	.empty-text {
		font-size: 12px;
		color: var(--text-3);
		margin: 12px 0;
	}

	.center-card {
		text-align: center;
		padding: 40px;
	}

	.loading-state {
		color: var(--text-2);
		font-size: 13px;
	}

	/* Responsive */
	@media (max-width: 1024px) {
		.audience-grid,
		.mini-grid,
		.kpi-grid,
		.health-strip {
			grid-template-columns: repeat(2, 1fr);
		}

		.funnel-container {
			grid-template-columns: 1fr;
			gap: 12px;
		}

		.funnel-divider {
			display: none;
		}

		.grid-2 {
			grid-template-columns: 1fr;
		}
	}

	@media (max-width: 640px) {
		.head {
			flex-direction: column;
			align-items: stretch;
		}

		.audience-grid,
		.mini-grid,
		.kpi-grid,
		.health-strip {
			grid-template-columns: 1fr;
		}
	}
</style>
