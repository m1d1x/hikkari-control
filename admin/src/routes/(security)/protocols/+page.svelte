<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import { formatBytes } from '$lib/utils/format';
	import {
		fetchProtocolStats,
		describeEntryError,
		formatCents,
		PROTO_LABEL,
		type EntryProtocolStats,
		type OverviewWindow,
		type ProtocolHealth,
		type ProtocolRelay,
	} from '$lib/api/entry';

	// Which protocol lives and how much it carries, in one look. Unlike the
	// overview this screen is an inventory on purpose. A figure the hub cannot
	// read is named in `missing` and shown as «нет данных», never as zero.

	const WINDOWS: OverviewWindow[] = ['24h', '7d', '30d'];
	const REFRESH_MS = 30_000;

	const HEALTH_LABEL: Record<ProtocolHealth, string> = {
		ok: 'проходит',
		warn: 'частично',
		bad: 'не проходит',
		unknown: 'нет вердикта',
	};

	const STATUS_LABEL: Record<string, string> = {
		alive: 'жив',
		pending: 'ожидает',
		testing: 'проверка',
		draining: 'выводится',
		dead: 'мёртв',
	};

	let data = $state<EntryProtocolStats | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<OverviewWindow>('24h');
	let timer: ReturnType<typeof setInterval> | undefined;

	const noData = $derived((data?.missing.length ?? 0) > 0);

	async function load() {
		try {
			data = await fetchProtocolStats(window);
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

	onMount(() => {
		void load();
		timer = setInterval(load, REFRESH_MS);
	});
	onDestroy(() => clearInterval(timer));

	const WINDOW_SECONDS: Record<OverviewWindow, number> = {
		'24h': 86_400,
		'7d': 604_800,
		'30d': 2_592_000,
	};

	type Cell = { key: number; cls: string; title: string };

	/** The relay's history across the whole window: a bucket nobody probed stays blank. */
	function strip(r: ProtocolRelay, bucketSeconds: number): Cell[] {
		const count = Math.ceil(WINDOW_SECONDS[window] / bucketSeconds);
		const last = Math.floor(Date.now() / 1000 / bucketSeconds);
		const byBucket = new Map(
			r.history.map((p) => [Math.floor(Date.parse(p.bucket) / 1000 / bucketSeconds), p]),
		);
		const cells: Cell[] = [];
		for (let i = count - 1; i >= 0; i--) {
			const key = last - i;
			const p = byBucket.get(key);
			if (!p) {
				cells.push({ key, cls: 'none', title: 'нет проб' });
				continue;
			}
			const cls = p.bad > 0 ? 'bad' : p.ok > 0 ? 'ok' : 'unknown';
			const at = new Date(key * bucketSeconds * 1000).toLocaleString('ru', {
				day: '2-digit',
				month: '2-digit',
				hour: '2-digit',
				minute: '2-digit',
			});
			cells.push({
				key,
				cls,
				title: `${at}: проходит ${p.ok}, не проходит ${p.bad}, прочее ${p.other}; худший вердикт ${p.worst}`,
			});
		}
		return cells;
	}

	function ago(at: string | undefined): string {
		if (!at) return '';
		const s = Math.max(0, Math.round((Date.now() - Date.parse(at)) / 1000));
		if (s < 90) return `${s} с назад`;
		if (s < 5400) return `${Math.round(s / 60)} мин назад`;
		return `${Math.round(s / 3600)} ч назад`;
	}
</script>

<PageShell>
	<div class="protocols-page">
		<div class="head">
			<h1>протоколы</h1>
			<div class="ranges">
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
		{:else if data && data.protocols.length === 0}
			<p class="hint">Во флоте нет машин.</p>
		{:else if data}
			{@const bucket = data.bucketSeconds}
			<div class="summary">
				{#each data.protocols as p (p.proto)}
					<a class="sum {p.health}" href="#{p.proto}">
						<div class="sum-top">
							<span class="dot"></span>
							<b>{PROTO_LABEL[p.proto] ?? p.proto}</b>
							<span class="sum-state">{HEALTH_LABEL[p.health]}</span>
						</div>
						<div class="sum-live">
							{p.boxesLive}<span>из {p.boxesTotal} живы</span>
						</div>
						<dl>
							<dt>трафик за {data.window}</dt>
							<dd>{noData ? 'нет данных' : formatBytes(p.bytesUp + p.bytesDown)}</dd>
							<dt>подключений</dt>
							<dd>{noData ? 'нет данных' : p.conns.toLocaleString('ru')}</dd>
							<dt>подписчиков</dt>
							<dd>{p.subscribers.toLocaleString('ru')}</dd>
							<dt>в месяц</dt>
							<dd>
								{formatCents(p.monthlyCents)}{#if p.boxesUnpriced > 0}
									<span class="note"> + {p.boxesUnpriced} без цены</span>
								{/if}
							</dd>
						</dl>
					</a>
				{/each}
			</div>

			{#each data.protocols as p (p.proto)}
				<section class="card" id={p.proto}>
					<div class="card-head">
						<h2>{PROTO_LABEL[p.proto] ?? p.proto}</h2>
						<span class="right">релеев живо {p.relaysAlive} из {p.relaysTotal}</span>
					</div>
					<div class="table-wrap">
						<table>
							<thead>
								<tr>
									<th>нода</th>
									<th>статус</th>
									<th>вердикт</th>
									<th class="hist-col">история за {data.window}</th>
									<th class="num">↓ трафик</th>
									<th class="num">↑ трафик</th>
									<th class="num">подключ.</th>
									<th class="num">подписч.</th>
									<th class="num">в месяц</th>
								</tr>
							</thead>
							{#each p.boxes as b (b.name)}
								<tbody>
									{#each b.relays as r, i (r.name)}
										<tr>
											<td>
												<span class="relay">{r.name}</span>
												<span class="sub">:{r.port || '?'}{#if i === 0 && b.domain} · {b.domain}{/if}</span>
											</td>
											<td class:muted={r.status !== 'alive'}>{STATUS_LABEL[r.status] ?? r.status}</td>
											<td>
												{#if r.verdict}
													<span class="verdict {r.health}">{r.verdict}</span>
													<span class="sub">{ago(r.verdictAt)}</span>
												{:else}
													<span class="nodata">нет данных</span>
												{/if}
											</td>
											<td class="hist-col">
												<div class="strip">
													{#each strip(r, bucket) as c (c.key)}
														<i class={c.cls} title={c.title}></i>
													{/each}
												</div>
											</td>
											<td class="num">{noData ? '—' : formatBytes(r.bytesDown)}</td>
											<td class="num">{noData ? '—' : formatBytes(r.bytesUp)}</td>
											<td class="num">
												{#if r.conns === null}
													<span class="nodata" title={r.pulseAt ? `последний пульс ${ago(r.pulseAt)}` : 'пульса не было'}>—</span>
												{:else}
													{r.conns.toLocaleString('ru')}
												{/if}
											</td>
											<td class="num">{r.subscribers.toLocaleString('ru')}</td>
											{#if i === 0}
												<td class="num box-cost" rowspan={b.relays.length}>
													{b.monthlyCents === null ? 'нет цены' : formatCents(b.monthlyCents)}
												</td>
											{/if}
										</tr>
									{:else}
										<tr>
											<td><span class="relay">{b.name}</span></td>
											<td colspan="7" class="muted">релеев нет</td>
											<td class="num">{b.monthlyCents === null ? 'нет цены' : formatCents(b.monthlyCents)}</td>
										</tr>
									{/each}
								</tbody>
							{/each}
						</table>
					</div>
				</section>
			{/each}

			{#each data.missing as m (m.figure)}
				<p class="hint">Нет данных ({m.figure}): {m.why}</p>
			{/each}
		{/if}
	</div>
</PageShell>

<style>
	.protocols-page {
		--card-bg: oklch(0.2 0 0);
		--ok: oklch(0.72 0.15 155);
		--warn: oklch(0.78 0.12 85);
		--bad: oklch(0.65 0.2 25);
		--unknown: oklch(0.52 0 0);
		--border-subtle: oklch(0.24 0 0);
		--text: oklch(0.93 0 0);
		--text-2: oklch(0.62 0 0);
		--text-3: oklch(0.5 0 0);
		--mono: 'IBM Plex Mono', monospace;

		color: var(--text);
		font-size: 13px;
		line-height: 1.5;
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		margin-bottom: 16px;
	}

	h1 {
		margin: 0;
		font-size: 18px;
		font-weight: 600;
	}

	.ranges {
		display: flex;
		gap: 4px;
	}

	.chip {
		min-height: 32px;
		padding: 4px 10px;
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
		background: none;
		color: var(--text-2);
		font: inherit;
		font-size: 12px;
		cursor: pointer;
	}

	.chip.on {
		background: oklch(0.26 0 0);
		color: var(--text);
	}

	.err {
		margin: 0 0 16px;
		color: var(--bad);
	}

	.hint {
		margin: 0 0 12px;
		color: var(--text-2);
	}

	.summary {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
		gap: 12px;
		margin-bottom: 16px;
	}

	.sum {
		--state: var(--unknown);
		display: block;
		padding: 14px 16px;
		background: var(--card-bg);
		border: 1px solid var(--border-subtle);
		border-top: 2px solid var(--state);
		border-radius: 8px;
		color: inherit;
		text-decoration: none;
	}

	.sum.ok {
		--state: var(--ok);
	}

	.sum.warn {
		--state: var(--warn);
	}

	.sum.bad {
		--state: var(--bad);
	}

	.sum-top {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.dot {
		width: 8px;
		height: 8px;
		flex: none;
		border-radius: 50%;
		background: var(--state);
	}

	.sum-state {
		margin-left: auto;
		font-size: 12px;
		color: var(--state);
	}

	.sum-live {
		margin: 8px 0 10px;
		font-size: 28px;
		font-weight: 600;
		line-height: 1.1;
		font-variant-numeric: tabular-nums;
	}

	.sum-live span {
		margin-left: 6px;
		font-size: 13px;
		font-weight: 400;
		color: var(--text-2);
	}

	dl {
		display: grid;
		grid-template-columns: 1fr auto;
		gap: 3px 12px;
		margin: 0;
	}

	dt {
		color: var(--text-2);
	}

	dd {
		margin: 0;
		text-align: right;
		font-variant-numeric: tabular-nums;
	}

	.note {
		font-size: 11px;
		color: var(--warn);
	}

	.card {
		margin-bottom: 16px;
		background: var(--card-bg);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		scroll-margin-top: 16px;
	}

	.card-head {
		display: flex;
		align-items: baseline;
		gap: 10px;
		padding: 12px 16px;
		border-bottom: 1px solid var(--border-subtle);
	}

	.card-head h2 {
		margin: 0;
		font-size: 13px;
		font-weight: 600;
	}

	.right {
		margin-left: auto;
		font-size: 12px;
		color: var(--text-2);
	}

	.table-wrap {
		overflow-x: auto;
	}

	table {
		width: 100%;
		min-width: 880px;
		border-collapse: collapse;
	}

	th {
		padding: 8px 12px;
		font-size: 11px;
		font-weight: 500;
		text-align: left;
		white-space: nowrap;
		color: var(--text-2);
	}

	td {
		padding: 7px 12px;
		border-top: 1px solid var(--border-subtle);
		white-space: nowrap;
	}

	.num {
		text-align: right;
		font-variant-numeric: tabular-nums;
	}

	.relay {
		font-family: var(--mono);
		font-size: 12px;
	}

	.sub {
		margin-left: 4px;
		font-size: 11px;
		color: var(--text-2);
	}

	.muted,
	.nodata {
		color: var(--text-3);
	}

	.verdict {
		font-family: var(--mono);
		font-size: 12px;
		color: var(--text-2);
	}

	.verdict.ok {
		color: var(--ok);
	}

	.verdict.bad {
		color: var(--bad);
	}

	.hist-col {
		width: 240px;
	}

	.strip {
		display: flex;
		gap: 1px;
		height: 14px;
		min-width: 200px;
	}

	.strip i {
		flex: 1 1 0;
		min-width: 1px;
		border-radius: 1px;
		background: oklch(0.27 0 0);
	}

	.strip i.ok {
		background: var(--ok);
	}

	.strip i.bad {
		background: var(--bad);
	}

	.strip i.unknown {
		background: var(--unknown);
	}

	.box-cost {
		vertical-align: top;
	}
</style>
