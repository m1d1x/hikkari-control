<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import TrendChart, { type TrendSeries } from '$components/entry/TrendChart.svelte';
	import { PROTO_LABEL } from '$lib/api/entry';
	import { ago } from '$lib/product/format';
	import {
		fetchTesterRelays,
		type TesterRelayScreen,
		type TesterRelays,
		type TesterRelayWindow,
	} from '$lib/api/support';

	// What the people behind the censor got from each relay: nekko's protocols
	// block, then history, then each tester. All of it is measured by testers'
	// clients; the fleet's own view of the same protocols is /protocols/servers.

	const WINDOWS: TesterRelayWindow[] = ['24h', '7d'];
	const REFRESH_MS = 60_000;
	const UP_LABEL: Record<string, string> = {
		none: '0 релеев',
		one: '1 релей',
		some: 'больше одного',
		all: 'все',
	};
	const UP_COLORS: Record<string, string> = { none: '#e66767', one: '#c98500', some: '#3987e5', all: '#199e70' };
	const FAIL_LABEL: Record<string, string> = {
		tcp_blocked: 'TCP не открывается',
		tcp_thinned: 'часть TCP-потоков теряется',
		protocol: 'TCP есть, протокол не проходит',
		timeout: 'нет ответа',
		error: 'ошибка проверки',
	};
	const FAIL_COLORS: Record<string, string> = {
		tcp_blocked: '#e66767',
		tcp_thinned: '#c98500',
		protocol: '#d55181',
		timeout: '#3987e5',
		error: '#9085e9',
	};

	let data = $state<TesterRelayScreen | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<TesterRelayWindow>('24h');
	let onlyBroken = $state(true);
	let opened = $state<string | null>(null);
	let timer: ReturnType<typeof setInterval> | undefined;

	async function load() {
		try {
			data = await fetchTesterRelays(window);
			loadError = null;
		} catch (e) {
			loadError = e instanceof Error ? e.message : 'не удалось загрузить';
		}
	}

	function pick(w: TesterRelayWindow) {
		if (w === window) return;
		window = w;
		void load();
	}

	onMount(() => {
		void load();
		timer = setInterval(load, REFRESH_MS);
	});
	onDestroy(() => clearInterval(timer));

	function protoColor(proto: string): string {
		if (proto.includes('xhttp')) return 'var(--c-xhttp)';
		if (proto.includes('reality')) return 'var(--c-reality)';
		if (proto.includes('hyst')) return 'var(--c-hyst)';
		return 'var(--c-other)';
	}
	const label = (proto: string) => PROTO_LABEL[proto] ?? proto;
	const upCount = (c: TesterRelays) => c.relays.filter((r) => r.up).length;
	const seen = (iso: string) => ago(new Date(iso).getTime());
	const clients = $derived(
		(data?.installs ?? []).filter((c) => !onlyBroken || upCount(c) < c.relays.length),
	);

	const percent = (v: number) => `${Math.round(v)}%`;
	const ms = (v: number) => `${Math.round(v)} мс`;
	const count = (v: number) => Math.round(v).toLocaleString('ru');

	const okLines = $derived<TrendSeries[]>(
		(data?.protocols ?? []).map((p) => ({
			key: p.proto,
			label: label(p.proto),
			color: protoColor(p.proto),
			values: p.okSeries,
			format: percent,
		})),
	);
	const pingLines = $derived<TrendSeries[]>(
		(data?.protocols ?? []).map((p) => ({
			key: p.proto,
			label: label(p.proto),
			color: protoColor(p.proto),
			values: p.pingSeries,
			format: ms,
		})),
	);
	const upLines = $derived<TrendSeries[]>(
		(data?.clientsByUp ?? []).map((s) => ({
			key: s.key,
			label: UP_LABEL[s.key] ?? s.key,
			color: UP_COLORS[s.key],
			values: s.values,
			format: percent,
		})),
	);
	const failLines = $derived<TrendSeries[]>(
		(data?.failures ?? []).map((s) => ({
			key: s.key,
			label: FAIL_LABEL[s.key] ?? s.key,
			color: FAIL_COLORS[s.key],
			values: s.values,
			format: count,
		})),
	);
	const charts = $derived([
		{ title: 'Проверки релеев у тестеров: прошло', lines: okLines, format: percent, max: 100 },
		{ title: 'Пинг у тестеров, p50', lines: pingLines, format: ms, max: undefined },
		{ title: 'Сколько релеев отвечает: доля тестеров', lines: upLines, format: percent, max: 100 },
		{ title: 'Почему релеи падают: число проверок', lines: failLines, format: count, max: undefined },
	]);
</script>

<PageShell>
	<div class="page">
		<div class="head">
			<h1>протоколы</h1>
			<div class="chips">
				<a class="chip" href="/protocols/servers">со стороны серверов</a>
				<a class="chip" href="/protocols/knobs">ручки релеев</a>
				<a class="chip" href="/protocols/hikkari">hikkari изнутри</a>
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
			<section class="card">
				<h2>протоколы</h2>
				{#if data.protocols.length === 0}
					<p class="hint">ни одной проверки релея за окно</p>
				{:else}
					<div class="ratio-section">
						<div class="ratio-bar">
							{#each data.protocols as p (p.proto)}
								<div class="ratio-seg" style="width: {p.sharePct}%; background: {protoColor(p.proto)}"></div>
							{/each}
						</div>
						<div class="ratio-legend">
							{#each data.protocols as p (p.proto)}
								<div class="legend-chip" title="{p.checks.toLocaleString('ru')} проверок">
									<span class="dot" style="background: {protoColor(p.proto)}"></span>
									<span class="chip-name">{label(p.proto)}</span>
									<b style="color: {protoColor(p.proto)}">{Math.round(p.sharePct)}%</b>
								</div>
							{/each}
						</div>
					</div>
					<div class="protos-grid">
						{#each data.protocols as p (p.proto)}
							<div class="proto-card" title="{p.checks.toLocaleString('ru')} проверок за окно">
								<div class="card-top">
									<div class="proto-badge">
										<span class="dot" style="background: {protoColor(p.proto)}"></span>
										<span class="proto-title">{label(p.proto)}</span>
									</div>
									<div class="proto-share" style="color: {protoColor(p.proto)}">
										{Math.round(p.sharePct)}<span class="share-unit">%</span>
									</div>
								</div>
								<div class="track">
									<div class="fill" style="width: {p.sharePct}%; background: {protoColor(p.proto)}"></div>
								</div>
								<div class="metrics">
									<div>
										<div class="val">{p.p50Ms ?? '-'}{#if p.p50Ms !== null}<small>мс</small>{/if}</div>
										<div class="lbl">пинг p50</div>
									</div>
									<div>
										<div class="val">{p.p95Ms ?? '-'}{#if p.p95Ms !== null}<small>мс</small>{/if}</div>
										<div class="lbl">пинг p95</div>
									</div>
									<div><div class="val">{Math.round(p.okPct)}<small>%</small></div><div class="lbl" title="доля проверок тестеров, на которые релей ответил">доходит</div></div>
									<div><div class="val">{Math.round(100 - p.okPct)}<small>%</small></div><div class="lbl" title="доля проверок без ответа (пинг через ядро, не потеря пакетов)">без ответа</div></div>
								</div>
							</div>
						{/each}
					</div>
				{/if}
			</section>

			<div class="grid">
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
							formatAxis={c.format}
							max={c.max}
							height={190}
						/>
					</section>
				{/each}
			</div>

			<section class="card">
				<div class="head">
					<h2>Тестеры</h2>
					<div class="chips">
						<button class="chip" class:on={onlyBroken} onclick={() => (onlyBroken = true)}>с проблемами</button>
						<button class="chip" class:on={!onlyBroken} onclick={() => (onlyBroken = false)}>все</button>
					</div>
				</div>
				{#if clients.length === 0}
					<p class="hint">{onlyBroken ? 'нет тестеров с упавшими релеями' : 'тестеры не присылали проверок за окно'}</p>
				{/if}
				{#each clients as c (c.install_id)}
					<button
						class="client"
						class:open={opened === c.install_id}
						onclick={() => (opened = opened === c.install_id ? null : c.install_id)}
					>
						<span class="cid" title={c.install_id}>{c.install_id}</span>
						<span class="cells" aria-label="отвечает {upCount(c)} из {c.relays.length}">
							{#each c.relays as r (r.tag)}
								<i class:up={r.up} title={r.tag}></i>
							{/each}
						</span>
						<b class:zero={upCount(c) === 0} class:low={upCount(c) === 1}>{upCount(c)} из {c.relays.length}</b>
						<span class="meta">{c.network ?? 'сеть неизвестна'}</span>
						<span class="meta">{c.platform} · {c.version}</span>
						<span class="meta ago">{seen(c.at)}</span>
					</button>
					{#if opened === c.install_id}
						<div class="table-wrap">
							<table>
								<thead>
									<tr>
										<th>релей</th><th>протокол</th><th>статус</th><th>причина</th><th class="num">TCP-потоки</th>
										<th class="num">пинг</th><th>последний успех</th>
									</tr>
								</thead>
								<tbody>
									{#each c.relays as r (r.tag)}
										<tr>
											<td class="mono">{r.tag}</td>
											<td><span class="dot" style="background: {protoColor(r.proto)}"></span> {label(r.proto)}</td>
											<td class={r.up ? 'ok' : 'bad'}>{r.up ? 'отвечает' : 'упал'}</td>
											<td>{r.reason ? (FAIL_LABEL[r.reason] ?? r.reason) : '-'}</td>
											<td class="num mono">{r.flowsOpen !== undefined ? `${r.flowsOpen}/${r.flowsTotal}` : '-'}</td>
											<td class="num mono">{r.pingMs ? `${r.pingMs} мс` : '-'}</td>
											<td>{r.lastOk ? seen(r.lastOk) : 'нет за окно'}</td>
										</tr>
									{/each}
								</tbody>
							</table>
						</div>
					{/if}
				{/each}
			</section>
		{/if}
	</div>
</PageShell>

<style>
	.page {
		--inset: oklch(0.17 0 0);
		--c-reality: oklch(0.75 0.14 350);
		--c-hyst: oklch(0.72 0.12 200);
		--c-xhttp: oklch(0.78 0.12 85);
		--c-other: oklch(0.72 0.16 290);
		--ok: oklch(0.72 0.15 155);
		--warn: oklch(0.78 0.12 85);
		--bad: oklch(0.65 0.2 25);
		--border: oklch(0.24 0 0);
		--border-subtle: oklch(0.19 0 0);
		--text: oklch(0.93 0 0);
		--text-2: oklch(0.62 0 0);
		--text-3: oklch(0.42 0 0);
		--mono: 'IBM Plex Mono', monospace;
		color: var(--text);
		font-size: 13px;
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
		margin: 0 0 12px;
		font-size: 14px;
		font-weight: 600;
	}

	.head h2 {
		margin: 0;
	}

	.err {
		margin: 0 0 12px;
		color: var(--bad);
	}

	.hint {
		margin: 0;
		color: var(--text-2);
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

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 420px), 1fr));
		gap: 12px;
	}

	.grid .card {
		margin-bottom: 0;
	}

	.grid {
		margin-bottom: 12px;
	}

	.ratio-section {
		margin-bottom: 14px;
		padding: 12px 14px;
		background: var(--inset);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
	}

	.ratio-bar {
		display: flex;
		gap: 2px;
		height: 9px;
		overflow: hidden;
		border-radius: 5px;
	}

	.ratio-seg {
		min-width: 4px;
	}

	.ratio-legend {
		display: flex;
		flex-wrap: wrap;
		gap: 8px 20px;
		margin-top: 10px;
		font-size: 11px;
	}

	.legend-chip,
	.proto-badge {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.dot {
		display: inline-block;
		flex-shrink: 0;
		width: 8px;
		height: 8px;
		border-radius: 2px;
	}

	.chip-name {
		font-family: var(--mono);
		color: var(--text-2);
	}

	.legend-chip b {
		font-family: var(--mono);
		font-size: 12px;
	}

	.protos-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));
		gap: 14px;
	}

	.proto-card {
		padding: 14px 16px;
		background: var(--inset);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
	}

	.card-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
	}

	.proto-title {
		font-family: var(--mono);
		font-weight: 600;
	}

	.proto-share {
		font-family: var(--mono);
		font-size: 24px;
		font-weight: 700;
		line-height: 1;
	}

	.share-unit {
		margin-left: 1px;
		font-size: 14px;
		font-weight: 400;
	}

	.track {
		height: 3px;
		margin: 10px 0 14px;
		overflow: hidden;
		border-radius: 2px;
		background: var(--border);
	}

	.fill {
		height: 100%;
	}

	.metrics {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 8px;
	}

	.val {
		font-family: var(--mono);
		font-size: 15px;
		font-weight: 600;
		white-space: nowrap;
	}

	.val small {
		margin-left: 2px;
		color: var(--text-2);
		font-size: 10px;
		font-weight: 400;
	}

	.val.muted {
		color: var(--text-3);
	}

	.lbl {
		margin-top: 3px;
		overflow: hidden;
		color: var(--text-2);
		font-size: 10px;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 14px;
		margin: -6px 0 6px;
		color: var(--text-2);
		font-size: 12px;
	}

	.legend span {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}

	.legend i {
		width: 12px;
		height: 3px;
		border-radius: 2px;
	}

	.client {
		display: grid;
		grid-template-columns: minmax(0, 150px) 130px 56px minmax(0, 1.2fr) minmax(0, 1fr) 96px;
		align-items: center;
		gap: 12px;
		width: 100%;
		min-height: 40px;
		padding: 6px 8px;
		border: none;
		border-top: 1px solid var(--border-subtle);
		background: none;
		color: var(--text);
		font: inherit;
		text-align: left;
		cursor: pointer;
	}

	.client:hover,
	.client.open {
		background: oklch(0.23 0 0);
	}

	.cid,
	.mono {
		font-family: var(--mono);
		font-size: 12px;
	}

	.cid {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.cells {
		display: flex;
		gap: 3px;
	}

	.cells i {
		width: 14px;
		height: 14px;
		border-radius: 3px;
		background: oklch(0.65 0.2 25 / 0.3);
	}

	.cells i.up {
		background: var(--ok);
	}

	.client b {
		font-family: var(--mono);
		font-size: 12px;
		white-space: nowrap;
	}

	.client b.low {
		color: var(--warn);
	}

	.client b.zero {
		color: var(--bad);
	}

	.meta {
		overflow: hidden;
		color: var(--text-2);
		font-size: 12px;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.ago {
		text-align: right;
	}

	.table-wrap {
		overflow-x: auto;
		padding: 4px 8px 12px;
		background: var(--inset);
	}

	table {
		width: 100%;
		min-width: 720px;
		border-collapse: collapse;
		font-size: 12px;
	}

	th {
		padding: 6px 8px;
		color: var(--text-2);
		font-weight: 500;
		text-align: left;
	}

	td {
		padding: 6px 8px;
		border-top: 1px solid var(--border-subtle);
	}

	.num {
		text-align: right;
	}

	.ok {
		color: var(--ok);
	}

	.bad {
		color: var(--bad);
	}

	@media (max-width: 800px) {
		.client {
			grid-template-columns: minmax(0, 1fr) 110px 56px;
		}

		.client .meta {
			display: none;
		}
	}
</style>
