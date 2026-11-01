<script lang="ts">
	import WinChips from '$components/product/WinChips.svelte';
	import EChart from '$components/charts/EChart.svelte';
	import EmptyChart from '$components/charts/EmptyChart.svelte';
	import type { EChartsOption } from '$lib/utils/echarts';
	import { fetchProductMetrics, type MetricsShare } from '$lib/api/product';
	import { num, pct, secs, bucketText } from '$lib/product/format';
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

	const or = (v: number | null, f: (v: number) => string) => (v === null ? '—' : f(v));
	const osColor = (s: MetricsShare) => OS_COLOR[s.key] ?? '#787878';
	const versionColor = (i: number) => VERSION_COLORS[i % VERSION_COLORS.length] ?? '#787878';

	function makeMetricChart(
		data: number[],
		labels: string[],
		color: string,
		opts?: { unit?: string },
	): EChartsOption {
		return {
			grid: { left: 36, right: 12, top: 10, bottom: 22 },
			xAxis: {
				type: 'category',
				data: labels,
				axisLabel: {
					show: true,
					fontSize: 10,
					color: '#787878',
					interval: Math.max(Math.floor(labels.length / 8) - 1, 0),
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
							return `${p.axisValue}<br/><b>${p.value}${opts.unit}</b>`;
						}
					: (params: any) => {
							const p = Array.isArray(params) ? params[0] : params;
							return `${p.axisValue}<br/><b>${p.value}</b>`;
						},
			},
			series: [
				{
					type: 'line',
					data,
					smooth: true,
					symbol: 'none',
					lineStyle: { width: 1.5, color },
					areaStyle: { opacity: 0.1, color },
				},
			],
		};
	}

	// Audience time series
	let dauData = $derived(d?.people?.dauTimeSeries?.data ?? []);
	let dauLabels = $derived(dauData.map((pt) => (pt.timestamp.length > 5 ? pt.timestamp.slice(5) : pt.timestamp)));
	let dauValues = $derived(dauData.map((pt) => pt.value));
	let dauChart = $derived(makeMetricChart(dauValues, dauLabels, '#45b97c'));

	let wauData = $derived(d?.people?.wauTimeSeries?.data ?? []);
	let wauLabels = $derived(wauData.map((pt) => (pt.timestamp.length > 5 ? pt.timestamp.slice(5) : pt.timestamp)));
	let wauValues = $derived(wauData.map((pt) => pt.value));
	let wauChart = $derived(makeMetricChart(wauValues, wauLabels, '#6192d4'));

	let mauData = $derived(d?.people?.mauTimeSeries?.data ?? []);
	let mauLabels = $derived(mauData.map((pt) => pt.timestamp));
	let mauValues = $derived(mauData.map((pt) => pt.value));
	let mauChart = $derived(makeMetricChart(mauValues, mauLabels, '#b07cd8'));

	let stickData = $derived(d?.people?.stickinessTimeSeries?.data ?? []);
	let stickLabels = $derived(stickData.map((pt) => (pt.timestamp.length > 5 ? pt.timestamp.slice(5) : pt.timestamp)));
	let stickValues = $derived(stickData.map((pt) => pt.value));
	let stickinessChart = $derived(makeMetricChart(stickValues, stickLabels, '#e8a838', { unit: '%' }));

	// Activity timeline chart (Active & New over window buckets)
	let bucketLabels = $derived((d?.buckets ?? []).map((t) => bucketText(t, m.win).axis));
	let bucketTips = $derived((d?.buckets ?? []).map((t) => bucketText(t, m.win).tip));

	let activityChart = $derived<EChartsOption>({
		tooltip: {
			trigger: 'axis',
			backgroundColor: '#262626',
			borderColor: '#3a3a3a',
			textStyle: { color: '#ededed', fontSize: 12 },
			formatter: (params: any) => {
				const arr = Array.isArray(params) ? params : [params];
				if (!arr.length) return '';
				const idx = arr[0].dataIndex;
				const header = bucketTips[idx] ?? arr[0].axisValue;
				let out = `<div style="font-weight:600;margin-bottom:6px;color:#bbb">${header}</div>`;
				for (const item of arr) {
					out += `<div style="display:flex;align-items:center;justify-content:space-between;gap:12px;min-width:140px;margin-top:2px">
						<span><span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${item.color};margin-right:6px"></span>${item.seriesName}</span>
						<b style="font-family:monospace">${item.value}</b>
					</div>`;
				}
				return out;
			},
		},
		legend: {
			show: true,
			top: 0,
			right: 12,
			textStyle: { color: '#989898', fontSize: 11 },
			icon: 'circle',
			itemWidth: 8,
			itemHeight: 8,
		},
		grid: { left: 40, right: 16, top: 32, bottom: 24 },
		xAxis: {
			type: 'category',
			data: bucketLabels,
			axisLabel: {
				fontSize: 10,
				color: '#787878',
				interval: Math.max(Math.floor(bucketLabels.length / 8) - 1, 0),
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
		series: [
			{
				name: 'Активные установки',
				type: 'line',
				data: d?.people.series.active ?? [],
				smooth: true,
				symbol: 'none',
				lineStyle: { width: 2, color: '#e8457c' },
				areaStyle: { opacity: 0.1, color: '#e8457c' },
			},
			{
				name: 'Новые установки',
				type: 'line',
				data: d?.people.series.fresh ?? [],
				smooth: true,
				symbol: 'none',
				lineStyle: { width: 1.5, color: '#45b97c' },
				areaStyle: { opacity: 0.08, color: '#45b97c' },
			},
		],
	});

	// Downloads dynamics chart
	let downloadsChart = $derived<EChartsOption>({
		tooltip: {
			trigger: 'axis',
			backgroundColor: '#262626',
			borderColor: '#3a3a3a',
			textStyle: { color: '#ededed', fontSize: 12 },
			formatter: (params: any) => {
				const arr = Array.isArray(params) ? params : [params];
				if (!arr.length) return '';
				const idx = arr[0].dataIndex;
				const header = bucketTips[idx] ?? arr[0].axisValue;
				let out = `<div style="font-weight:600;margin-bottom:6px;color:#bbb">${header}</div>`;
				for (const item of arr) {
					out += `<div style="display:flex;align-items:center;justify-content:space-between;gap:12px;min-width:140px;margin-top:2px">
						<span><span style="display:inline-block;width:8px;height:8px;border-radius:50%;background:${item.color};margin-right:6px"></span>${item.seriesName}</span>
						<b style="font-family:monospace">${item.value}</b>
					</div>`;
				}
				return out;
			},
		},
		legend: {
			show: true,
			top: 0,
			right: 12,
			textStyle: { color: '#989898', fontSize: 11 },
			icon: 'circle',
			itemWidth: 8,
			itemHeight: 8,
		},
		grid: { left: 40, right: 16, top: 32, bottom: 24 },
		xAxis: {
			type: 'category',
			data: bucketLabels,
			axisLabel: {
				fontSize: 10,
				color: '#787878',
				interval: Math.max(Math.floor(bucketLabels.length / 8) - 1, 0),
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
		series: [
			...(d?.downloads?.clicksSeries
				? [
						{
							name: 'Клики «Скачать»',
							type: 'line' as const,
							data: d.downloads.clicksSeries,
							smooth: true,
							symbol: 'none',
							lineStyle: { width: 2, color: '#6192d4' },
							areaStyle: { opacity: 0.1, color: '#6192d4' },
						},
					]
				: []),
			{
				name: 'Первые запуски',
				type: 'line' as const,
				data: d?.people.series.fresh ?? [],
				smooth: true,
				symbol: 'none',
				lineStyle: { width: 1.5, color: '#b07cd8' },
				areaStyle: { opacity: 0.08, color: '#b07cd8' },
			},
		],
	});
</script>

<div class="head">
	<div class="head-info">
		<h1>Метрики продукта</h1>
		<span class="subtitle">Аудитория, активность туннелей, воронка конверсии и платформы</span>
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
			<span class="section-desc">Уникальные пользователи за сутки, неделю, месяц и динамика липкости</span>
		</div>
		<div class="audience-grid">
			<div class="chart-card">
				<div class="card-header">
					<div>
						<h3 class="card-title">DAU</h3>
						<span class="card-hint">за сутки · история 30 дней</span>
					</div>
					<span class="card-value mono">{num(d.people.dau)}</span>
				</div>
				{#if dauValues.length >= 1}
					<EChart option={dauChart} height="135px" />
				{:else}
					<EmptyChart note="Нет данных" height="135px" />
				{/if}
			</div>

			<div class="chart-card">
				<div class="card-header">
					<div>
						<h3 class="card-title">WAU</h3>
						<span class="card-hint">за неделю · история 12 недель</span>
					</div>
					<span class="card-value mono">{num(d.people.wau)}</span>
				</div>
				{#if wauValues.length >= 1}
					<EChart option={wauChart} height="135px" />
				{:else}
					<EmptyChart note="Нет данных" height="135px" />
				{/if}
			</div>

			<div class="chart-card">
				<div class="card-header">
					<div>
						<h3 class="card-title">MAU</h3>
						<span class="card-hint">за месяц · история 6 месяцев</span>
					</div>
					<span class="card-value mono">{num(d.people.mau)}</span>
				</div>
				{#if mauValues.length >= 1}
					<EChart option={mauChart} height="135px" />
				{:else}
					<EmptyChart note="Нет данных" height="135px" />
				{/if}
			</div>

			<div class="chart-card">
				<div class="card-header">
					<div>
						<h3 class="card-title">Липкость</h3>
						<span class="card-hint">DAU / MAU · история 30 дней</span>
					</div>
					<span class="card-value mono">{or(d.people.stickiness, pct)}</span>
				</div>
				{#if stickValues.length >= 1}
					<EChart option={stickinessChart} height="135px" />
				{:else}
					<EmptyChart note="Нет данных" height="135px" />
				{/if}
			</div>
		</div>
	</section>

	<!-- 2. Секция активности установок и сессий -->
	<section class="card content-card">
		<div class="section-header">
			<div>
				<h2 class="section-title">Активность установок</h2>
				<span class="section-desc">Запуски клиентов и возвращаемость пользователей за окно</span>
			</div>
		</div>

		<!-- 4 плитки ключевых показателей -->
		<div class="kpi-grid">
			<div class="kpi-box">
				<span class="kpi-label">Активные за период</span>
				<div class="kpi-main">
					<span class="kpi-num mono">{num(d.people.active)}</span>
					<span class="kpi-tag active-tag">запуск клиента</span>
				</div>
				<span class="kpi-sub">пользовались VPN в выбранном окне</span>
			</div>

			<div class="kpi-box">
				<span class="kpi-label">Новые установки</span>
				<div class="kpi-main">
					<span class="kpi-num mono text-ok">{num(d.people.fresh)}</span>
					<span class="kpi-tag new-tag">приток</span>
				</div>
				<span class="kpi-sub">первый запуск в этом окне</span>
			</div>

			<div class="kpi-box">
				<span class="kpi-label">Вернувшиеся</span>
				<div class="kpi-main">
					<span class="kpi-num mono">{num(d.people.returning)}</span>
					<span class="kpi-tag returning-tag">удержание</span>
				</div>
				<span class="kpi-sub">установлены ранее и вернулись</span>
			</div>

			<div class="kpi-box">
				<span class="kpi-label">Всего установок</span>
				<div class="kpi-main">
					<span class="kpi-num mono">{num(d.people.total)}</span>
					<span class="kpi-tag total-tag">за всё время</span>
				</div>
				<span class="kpi-sub">накопительный итог базы</span>
			</div>
		</div>

		<!-- Интерактивный график активности -->
		<div class="chart-wrapper">
			<EChart option={activityChart} height="200px" />
		</div>

		<!-- Метрики соединения и туннеля -->
		<div class="health-strip">
			<div class="health-box">
				<span class="h-label">Успешность соединений</span>
				<span class="h-val mono">{or(d.connect.okPct, pct)}</span>
				<span class="h-sub">из {num(d.connect.attempts)} попыток</span>
			</div>
			<div class="health-box">
				<span class="h-label">Скорость подключения</span>
				<span class="h-val mono">{or(d.connect.secsP50, secs)}</span>
				<span class="h-sub">медиана запуска VPN</span>
			</div>
			<div class="health-box">
				<span class="h-label">Время в VPN за сутки</span>
				<span class="h-val mono">{or(d.connect.tunnelMinutesP50, (v) => `${Math.round(v)} мин`)}</span>
				<span class="h-sub">медиана на активного юзера</span>
			</div>
			<div class="health-box">
				<span class="h-label">Всего попыток VPN</span>
				<span class="h-val mono">{num(d.connect.attempts)}</span>
				<span class="h-sub">запросов к провайдерам</span>
			</div>
		</div>
	</section>

	<!-- 3. Воронка конверсии онбординга: клик -> запуск -> первый туннель -->
	<section class="card content-card">
		<div class="section-header">
			<div>
				<h2 class="section-title">Воронка конверсии</h2>
				<span class="section-desc">От клика кнопки «Скачать» на сайте до первого успешного туннеля</span>
			</div>
		</div>

		<div class="funnel-container">
			<!-- Шаг 1 -->
			<div class="funnel-step">
				<div class="step-header">
					<span class="step-badge">Шаг 1</span>
					<span class="step-name">Клик «Скачать»</span>
				</div>
				<div class="step-num mono">{or(d.downloads.clicks, num)}</div>
				<span class="step-desc">переходов по ссылкам загрузки на сайте</span>
				{#if d.downloads.clicks === null}
					<div class="step-alert warning">Ожидание кликов с лендинга</div>
				{:else}
					<div class="step-alert success">Телеметрия подключена</div>
				{/if}
			</div>

			<div class="funnel-divider">
				<div class="arrow-icon">→</div>
			</div>

			<!-- Шаг 2 -->
			<div class="funnel-step">
				<div class="step-header">
					<span class="step-badge">Шаг 2</span>
					<span class="step-name">Первый запуск</span>
				</div>
				<div class="step-num mono">{num(d.downloads.installs)}</div>
				<span class="step-desc">новых установок клиента</span>
				<div class="step-kpi">
					<span class="kpi-pill">Конверсия: <b>{or(d.downloads.convPct, pct)}</b></span>
				</div>
			</div>

			<div class="funnel-divider">
				<div class="arrow-icon">→</div>
			</div>

			<!-- Шаг 3 -->
			<div class="funnel-step">
				<div class="step-header">
					<span class="step-badge">Шаг 3</span>
					<span class="step-name">Активация туннеля</span>
				</div>
				<div class="step-num mono">
					{num(d.activation.connected)}
					<span class="step-sub-num">из {num(d.activation.installs)}</span>
				</div>
				<span class="step-desc">успешно подняли первый туннель</span>
				<div class="step-kpi">
					<span class="kpi-pill text-ok">Конверсия: <b>{or(d.activation.pct, pct)}</b></span>
					{#if d.activation.secsP50 !== null}
						<span class="time-sub">до туннеля: <b>{secs(d.activation.secsP50)}</b> (p50)</span>
					{/if}
				</div>
			</div>
		</div>

		<!-- График динамики воронки -->
		{#if d.downloads.clicks !== null || d.downloads.installs > 0}
			<div class="chart-wrapper mt-16">
				<EChart option={downloadsChart} height="180px" />
			</div>
		{/if}

		<!-- Сплит платформ при скачивании -->
		{#if d.downloads.platforms.length > 0}
			<div class="breakdown-section">
				<span class="breakdown-title">Установки по операционным системам:</span>
				<div class="bar">
					{#each d.downloads.platforms as p (p.key)}
						<i style="width: {p.pct}%; background: {osColor(p)}"></i>
					{/each}
				</div>
				<div class="chips-list">
					{#each d.downloads.platforms as p (p.key)}
						<span class="chip-item">
							<i class="dot" style="background: {osColor(p)}"></i>
							<span class="chip-name">{p.key}</span>
							<b>{num(p.installs)}</b>
							<span class="dim">· {p.pct.toFixed(0)}%</span>
						</span>
					{/each}
				</div>
			</div>
		{/if}
	</section>

	<!-- 4. Секции платформ и версий клиента -->
	<div class="grid-2">
		<section class="card content-card">
			<div class="section-header">
				<div>
					<h2 class="section-title">Операционные системы</h2>
					<span class="section-desc">Доли ОС среди активных пользователей</span>
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
					<span class="section-desc">Используемые релизы приложения</span>
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
		grid-template-columns: repeat(4, 1fr);
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
		grid-template-columns: repeat(4, 1fr);
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
		.kpi-grid,
		.health-strip {
			grid-template-columns: 1fr;
		}
	}
</style>
