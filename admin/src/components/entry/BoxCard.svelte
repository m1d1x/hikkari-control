<script lang="ts">
	import TrendChart from '$components/entry/TrendChart.svelte';
	import ProtoBadge from '$components/entry/ProtoBadge.svelte';
	import {
		fetchBoxCard,
		describeEntryError,
		deathLabel,
		formatCents,
		CAUSE_LABEL,
		PLAYBOOK_LABEL,
		type EntryBoxCard,
		type OverviewWindow,
	} from '$lib/api/entry';

	/**
	 * One machine's card (roadmap 42 П9.3): its life, what it cost, and what
	 * its relays say in the pulse. One request; the address is opened by the
	 * server for this card only and shown when asked for.
	 */
	interface Props {
		name: string;
		onclose: () => void;
	}

	let { name, onclose }: Props = $props();

	let card = $state<EntryBoxCard | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<OverviewWindow>('7d');
	let showAddress = $state(false);
	const WINDOWS: { id: OverviewWindow; label: string }[] = [
		{ id: '24h', label: '24 ч' },
		{ id: '7d', label: '7 дн' },
		{ id: '30d', label: '30 дн' },
	];

	async function load() {
		try {
			card = await fetchBoxCard(name, window);
			loadError = null;
		} catch (e) {
			loadError = describeEntryError(e);
		}
	}

	$effect(() => {
		// Re-read whenever the machine or the window changes.
		void name;
		void window;
		showAddress = false;
		void load();
	});

	const missingOf = $derived(new Map((card?.missing ?? []).map((m) => [m.figure, m.why])));
	const series = $derived(card?.stats?.series ?? []);
	const buckets = $derived(series.map((p) => p.bucket));

	function when(iso: string | null | undefined): string {
		if (!iso) return '';
		return new Date(iso).toLocaleString('ru-RU', {
			day: '2-digit',
			month: '2-digit',
			hour: '2-digit',
			minute: '2-digit',
		});
	}

	function span(sec: number): string {
		const days = Math.floor(sec / 86400);
		const hours = Math.floor((sec % 86400) / 3600);
		if (days > 0) return `${days} дн ${hours} ч`;
		if (hours > 0) return `${hours} ч`;
		return `${Math.max(1, Math.floor(sec / 60))} мин`;
	}

	const mb = (v: number) => (v >= 1024 ? `${(v / 1024).toFixed(2)} ГБ` : `${v.toFixed(v < 10 ? 2 : 0)} МБ`);
</script>

<div class="backdrop" role="presentation" onclick={onclose}></div>
<aside class="box-card" aria-label="карточка машины {name}">
	<header class="head">
		<div class="title">
			<h2>{name}</h2>
			{#if card}
				<span class="sub">{card.box.provider} · {card.box.providerRegion || card.box.region || card.box.location}</span>
				<ProtoBadge proto={card.box.proto} />
			{/if}
		</div>
		<div class="ranges">
			{#each WINDOWS as w (w.id)}
				<button class="chip" class:on={window === w.id} onclick={() => (window = w.id)}>{w.label}</button>
			{/each}
		</div>
		<button class="chip" onclick={onclose}>закрыть</button>
	</header>

	{#if loadError}
		<p class="err" role="alert">{loadError}</p>
	{:else if !card}
		<p class="hint">загружаю карточку…</p>
	{:else}
		{@const h = card.history}
		<section class="facts">
			<div class="fact">
				<span class="label">ip</span>
				{#if card.box.addressUnreadable}
					<span class="nodata" title="ключ сервера не открывает запись">не читается</span>
				{:else if showAddress}
					<b class="m">{card.box.address}</b>
				{:else}
					<button class="link" onclick={() => (showAddress = true)}>открыть из конверта</button>
				{/if}
			</div>
			<div class="fact">
				<span class="label">срок жизни</span>
				<b class="m">{span(h.lifetimeSec)}</b>
			</div>
			<div class="fact">
				<span class="label">аренда за жизнь</span>
				{#if h.rentCents === null}
					<span class="nodata" title={missingOf.get('history.rentCents') ?? ''}>нет цены</span>
				{:else}
					<b class="m">{formatCents(h.rentCents)}</b>
				{/if}
			</div>
			<div class="fact">
				<span class="label">ремонты</span>
				<b class="m">{formatCents(h.repairCents)}</b>
			</div>
			<div class="fact">
				<span class="label">тариф</span>
				{#if card.box.monthlyCents == null}
					<span class="nodata">нет цены</span>
				{:else}
					<b class="m">{formatCents(card.box.monthlyCents)}<small>/мес</small></b>
				{/if}
			</div>
			<div class="fact">
				<span class="label">людей через машину</span>
				{#if card.people === null}
					<span class="nodata" title={missingOf.get('people') ?? ''}>нет данных</span>
				{:else}
					<b class="m">{card.people}</b>
				{/if}
			</div>
			{#if card.box.domain}
				<div class="fact">
					<span class="label">домен</span>
					<b class="m">{card.box.domain}</b>
				</div>
			{/if}
		</section>

		<section class="block">
			<h3>история</h3>
			<ol class="timeline">
				<li><span class="dot ok"></span><span class="ev">рождена</span><span class="at">{when(h.bornAt)}</span></li>
				{#if h.admittedAt}
					<li>
						<span class="dot ok"></span><span class="ev">допущена в пул</span><span class="at"
							>{when(h.admittedAt)}</span
						>
					</li>
				{:else}
					<li><span class="dot mute"></span><span class="ev muted">в пул не допускалась</span></li>
				{/if}
				{#each [...h.incidents].reverse() as inc (inc.id)}
					<li>
						<span class="dot {inc.closedAt ? 'warn' : 'bad'}"></span>
						<span class="ev">
							{CAUSE_LABEL[inc.cause] ?? inc.cause}
							{#if inc.playbook}<span class="muted">· {PLAYBOOK_LABEL[inc.playbook] ?? inc.playbook}</span>{/if}
							{#if inc.costCents > 0}<span class="muted">· {formatCents(inc.costCents)}</span>{/if}
							{#if !inc.closedAt}<span class="open-tag">открыт</span>{/if}
						</span>
						<span class="at">{when(inc.openedAt)}{inc.closedAt ? ` → ${when(inc.closedAt)}` : ''}</span>
					</li>
				{/each}
				{#if h.retiredAt}
					<li>
						<span class="dot bad"></span>
						<span class="ev">сдана · {deathLabel(h.retiredReason) || h.retiredReason}</span>
						<span class="at">{when(h.retiredAt)}</span>
					</li>
				{/if}
			</ol>
		</section>

		<section class="block">
			<h3>релеи</h3>
			{#if card.relays.length === 0}
				<p class="hint">на машине нет релеев</p>
			{:else}
				<div class="relays">
					<div class="r-head">
						<span>релей</span><span>протокол</span><span>статус</span><span class="num">держатели</span><span
							>версия</span
						><span>пульс</span>
					</div>
					{#each card.relays as r (r.name)}
						<div class="r-row">
							<span class="m"><i class="dot {r.verdict}"></i>{r.name}</span>
							<span>
								{#if r.unreadable}
									<span class="nodata">не читается</span>
								{:else}
									<ProtoBadge proto={r.proto} port={r.port} />
								{/if}
							</span>
							<span>{r.status}{r.diedReason ? ` · ${deathLabel(r.diedReason)}` : ''}</span>
							<span class="num m">{r.grants}</span>
							<span class="m">{r.version || 'нет'}</span>
							<span class="m">
								{#if r.lastPulseAt}
									{when(r.lastPulseAt)}{#if r.uptimeS != null}<small> · up {span(r.uptimeS)}</small>{/if}
								{:else}
									<span class="nodata">нет пульса</span>
								{/if}
							</span>
						</div>
					{/each}
				</div>
			{/if}
		</section>

		<section class="block">
			<h3>пульс</h3>
			{#if card.stats === null}
				<p class="hint">{missingOf.get('stats') ?? 'нет данных'}</p>
			{:else}
				<div class="charts">
					<div class="chart">
						<span class="c-title">трафик</span>
						<TrendChart
							{buckets}
							bucketSeconds={card.bucketSeconds}
							height={140}
							formatAxis={mb}
							series={[
								{
									key: 'down',
									label: 'к людям',
									color: 'oklch(0.72 0.12 200)',
									primary: true,
									values: series.map((p) => p.mbDown),
									format: mb,
								},
								{
									key: 'up',
									label: 'от людей',
									color: 'oklch(0.75 0.14 350)',
									values: series.map((p) => p.mbUp),
									format: mb,
								},
							]}
						/>
					</div>
					<div class="chart">
						<span class="c-title">новые соединения</span>
						<TrendChart
							{buckets}
							bucketSeconds={card.bucketSeconds}
							height={140}
							formatAxis={(v) => `${v.toFixed(2)}/с`}
							series={[
								{
									key: 'conns',
									label: 'в секунду',
									color: 'oklch(0.72 0.15 155)',
									primary: true,
									values: series.map((p) => p.connsPerSec),
									format: (v) => `${v.toFixed(2)}/с`,
								},
							]}
						/>
					</div>
					<div class="chart">
						<span class="c-title">cpu</span>
						<TrendChart
							{buckets}
							bucketSeconds={card.bucketSeconds}
							height={120}
							formatAxis={(v) => `${Math.round(v)}%`}
							series={[
								{
									key: 'cpu',
									label: 'cpu, среднее',
									color: 'oklch(0.78 0.12 85)',
									primary: true,
									values: series.map((p) => p.cpuPct),
									format: (v) => `${v.toFixed(1)}%`,
								},
							]}
						/>
					</div>
					<div class="chart">
						<span class="c-title">память</span>
						<TrendChart
							{buckets}
							bucketSeconds={card.bucketSeconds}
							height={120}
							formatAxis={(v) => `${Math.round(v)} МБ`}
							series={[
								{
									key: 'mem',
									label: 'ram, пик',
									color: 'oklch(0.7 0.1 280)',
									primary: true,
									values: series.map((p) => p.memMb),
									format: (v) => `${Math.round(v)} МБ`,
								},
							]}
						/>
					</div>
				</div>
			{/if}
			<div class="absent">
				<span title={missingOf.get('stats.activeSessions') ?? ''}
					>активные сессии: <span class="nodata">нет данных</span></span
				>
				<span title={missingOf.get('stats.handshakeFailures') ?? ''}
					>отказы рукопожатия: <span class="nodata">нет данных</span></span
				>
			</div>
		</section>
	{/if}
</aside>

<style>
	.backdrop {
		position: fixed;
		inset: 0;
		background: rgba(0, 0, 0, 0.55);
		z-index: 900;
	}

	.box-card {
		position: fixed;
		top: 0;
		right: 0;
		bottom: 0;
		width: min(920px, 100vw);
		overflow-y: auto;
		z-index: 901;
		background: var(--bg-raised, oklch(0.17 0 0));
		border-left: 1px solid var(--border, oklch(0.24 0 0));
		color: var(--text, oklch(0.93 0 0));
		font-size: 13px;
	}

	.head {
		position: sticky;
		top: 0;
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 14px 18px;
		background: var(--bg-raised, oklch(0.17 0 0));
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
		z-index: 1;
	}

	.title {
		display: flex;
		align-items: baseline;
		gap: 10px;
		margin-right: auto;
		min-width: 0;
	}

	.title h2 {
		margin: 0;
		font-family: var(--mono, monospace);
		font-size: 15px;
		font-weight: 600;
	}

	.sub {
		font-size: 12px;
		color: var(--text-2, oklch(0.62 0 0));
	}

	.ranges {
		display: flex;
		gap: 4px;
	}

	.chip {
		padding: 4px 10px;
		border-radius: 6px;
		font-size: 11px;
		color: var(--text-2, oklch(0.62 0 0));
		border: 1px solid var(--border, oklch(0.24 0 0));
		background: none;
		cursor: pointer;
		white-space: nowrap;
	}

	.chip.on {
		color: var(--text, oklch(0.93 0 0));
		background: oklch(0.75 0.14 350 / 0.1);
		border-color: oklch(0.75 0.14 350 / 0.35);
	}

	.facts {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
		gap: 1px;
		background: var(--border-subtle, oklch(0.19 0 0));
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
	}

	.fact {
		display: flex;
		flex-direction: column;
		gap: 3px;
		padding: 10px 16px;
		background: var(--bg-raised, oklch(0.17 0 0));
	}

	.fact .label {
		font-size: 11px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.fact b {
		font-size: 15px;
		font-weight: 600;
	}

	.fact small {
		font-size: 11px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.block {
		padding: 14px 18px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
	}

	.block h3 {
		margin: 0 0 10px;
		font-size: 12px;
		font-weight: 600;
		color: var(--text-2, oklch(0.62 0 0));
	}

	.timeline {
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.timeline li {
		display: grid;
		grid-template-columns: 14px 1fr auto;
		align-items: center;
		gap: 8px;
		padding: 4px 0;
	}

	.ev {
		min-width: 0;
	}

	.at {
		font-family: var(--mono, monospace);
		font-size: 11px;
		color: var(--text-3, oklch(0.42 0 0));
		white-space: nowrap;
	}

	.open-tag {
		margin-left: 6px;
		padding: 1px 6px;
		border-radius: 4px;
		font-size: 10px;
		color: var(--bad, oklch(0.65 0.2 25));
		background: oklch(0.65 0.2 25 / 0.12);
	}

	.relays .r-head,
	.relays .r-row {
		display: grid;
		grid-template-columns: 1.3fr 1.1fr 1.2fr 70px 0.9fr 1.3fr;
		align-items: center;
		gap: 10px;
	}

	.relays .r-head {
		padding-bottom: 5px;
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.relays .r-row {
		padding: 5px 0;
		font-size: 12px;
		border-top: 1px solid var(--border-subtle, oklch(0.19 0 0));
	}

	.relays small {
		color: var(--text-3, oklch(0.42 0 0));
	}

	.num {
		text-align: right;
	}

	.charts {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 12px 18px;
	}

	.chart {
		min-width: 0;
	}

	.c-title {
		display: block;
		margin-bottom: 2px;
		font-size: 11px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.absent {
		display: flex;
		gap: 22px;
		margin-top: 10px;
		font-size: 11px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.dot {
		display: inline-block;
		width: 6px;
		height: 6px;
		margin-right: 6px;
		border-radius: 50%;
		background: var(--unknown, oklch(0.52 0 0));
	}

	.dot.ok {
		background: var(--ok, oklch(0.72 0.15 155));
	}
	.dot.warn {
		background: var(--warn, oklch(0.78 0.12 85));
	}
	.dot.bad {
		background: var(--bad, oklch(0.65 0.2 25));
	}

	.m {
		font-family: var(--mono, monospace);
		font-variant-numeric: tabular-nums;
	}

	.muted {
		color: var(--text-3, oklch(0.42 0 0));
	}

	.nodata {
		color: var(--text-3, oklch(0.42 0 0));
		font-style: italic;
	}

	.link {
		padding: 0;
		font-size: 12px;
		color: var(--primary, oklch(0.75 0.14 350));
		background: none;
		border: none;
		cursor: pointer;
		text-align: left;
	}

	.hint {
		margin: 14px 18px;
		font-size: 12px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.block .hint {
		margin: 0;
	}

	.err {
		margin: 14px 18px;
		font-size: 12px;
		color: var(--bad, oklch(0.65 0.2 25));
	}

	@media (max-width: 760px) {
		.charts {
			grid-template-columns: 1fr;
		}

		.relays .r-head,
		.relays .r-row {
			grid-template-columns: 1fr 1fr 1fr;
		}

		.relays .r-head > span:nth-child(n + 4),
		.relays .r-row > span:nth-child(n + 4) {
			display: none;
		}
	}
</style>
