<script lang="ts">
	import { onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import Panel from '$components/entry/ui/Panel.svelte';
	import MetricHero from '$components/entry/ui/MetricHero.svelte';
	import RangeChips from '$components/entry/ui/RangeChips.svelte';
	import TimeSeries from '$components/entry/ui/TimeSeries.svelte';
	import type { HeroRow } from '$components/entry/ui/MetricHero.svelte';
	import { CHART, ZONE_COLORS } from '$components/entry/ui/colors';
	import {
		fetchDomains,
		fetchDomainMetrics,
		suggestDomains,
		checkoutDomain,
		buyDomain,
		seedDomain,
		fetchExclusions,
		excludeDomain,
		unexcludeDomain,
		fetchAccountDomains,
		describeProviderError,
		formatCents,
		type DomainLedgerRow,
		type DomainSpend,
		type DomainStatus,
		type DomainWindow,
		type DomainOffer,
		type DomainCheckout,
		type DomainExclusion,
		type AccountDomain,
		type EntryDomainMetrics,
		DOMAIN_SHAPE_LABEL,
	} from '$lib/api/entry';

	// The domain screen (roadmap 42 К4).
	//
	// It answers one question the overview does not: what to buy next, and
	// whether the buffer lasts until then. The overview already carries the
	// median life of a name and the daily spend on names, so both were cut
	// from here — the same number on two screens is one number nobody trusts.
	// What is left is the pool's drain, the comparison the buying decision
	// actually rests on (which zone and which kind of name survives longer),
	// and the tools: the ledger, the never-touch list and a manual purchase.
	//
	// Nothing here computes a figure: every number comes from
	// GET /entry/domains/metrics or the ledger handle, and what the hub
	// cannot compute is drawn as "нет данных" with the hub's own reason.

	const WINDOWS: readonly { key: DomainWindow; label: string }[] = [
		{ key: '7d', label: '7 дн' },
		{ key: '30d', label: '30 дн' },
		{ key: '90d', label: '90 дн' },
	];

	let range = $state<DomainWindow>('30d');
	let metrics = $state<EntryDomainMetrics | null>(null);
	let domains = $state<DomainLedgerRow[] | null>(null);
	let spend = $state<DomainSpend | null>(null);
	let exclusions = $state<DomainExclusion[]>([]);
	let accountDomains = $state<AccountDomain[]>([]);
	let loadError = $state<string | null>(null);

	type FilterType = 'all' | DomainStatus;
	let filter = $state<FilterType>('all');

	let buyName = $state('');
	let offers = $state<DomainOffer[]>([]);
	let checkout = $state<DomainCheckout | null>(null);
	let buyBusy = $state<'suggest' | 'check' | 'buy' | 'seed' | null>(null);
	let buyError = $state<string | null>(null);
	let buyNote = $state<string | null>(null);
	let confirmingBuy = $state(false);

	let ledgerOpen = $state(false);
	let addingExcl = $state(false);
	let newExclDomain = $state('');
	let exclBusy = $state(false);
	let seedingAll = $state(false);

	/** A figure the hub named as missing, with its reason. */
	const missingOf = $derived(new Map((metrics?.missing ?? []).map((m) => [m.figure, m.why])));

	const counts = $derived.by(() => {
		const c: Partial<Record<DomainStatus, number>> = {};
		for (const d of domains ?? []) c[d.status] = (c[d.status] ?? 0) + 1;
		return c;
	});

	const shownLedger = $derived(filter === 'all' ? (domains ?? []) : (domains ?? []).filter((d) => d.status === filter));

	const freeAccountDomains = $derived((accountDomains ?? []).filter((d) => !d.hubStatus && !d.blocked));

	const windowDays = $derived(range === '7d' ? 7 : range === '90d' ? 90 : 30);

	// ---------- 1. what survives longer: the buying decision ----------

	const months = ['янв', 'фев', 'мар', 'апр', 'май', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'];

	function formatDay(iso: string): string {
		const parts = iso.split('-');
		if (parts.length !== 3) return iso;
		return `${parseInt(parts[2], 10)} ${months[parseInt(parts[1], 10) - 1] ?? parts[1]}`;
	}

	/** Hours as the screen says them: short spans in hours, long ones in days. */
	function life(hours: number | null): string {
		if (hours === null) return 'мало данных';
		return hours < 48 ? `${Math.round(hours)} ч` : `${(hours / 24).toFixed(1)} дн`;
	}

	/** The widest sample sets the bar's full width, so the bars compare. */
	const longestLife = $derived.by(() => {
		const all = [
			...(metrics?.life.zones ?? []).map((z) => z.medianHours),
			...(metrics?.shapes ?? []).map((sh) => sh.medianHours),
		].filter((v): v is number => v !== null);
		return all.length > 0 ? Math.max(...all) : 0;
	});

	const zoneRanking = $derived.by(() =>
		[...(metrics?.life.zones ?? [])]
			.sort((a, b) => (b.medianHours ?? -1) - (a.medianHours ?? -1))
			.map((z, i) => ({
				name: z.tld,
				hours: z.medianHours,
				samples: z.sampleCount,
				note: '',
				color: ZONE_COLORS[i % ZONE_COLORS.length],
			})),
	);

	const shapeRanking = $derived.by(() =>
		(metrics?.shapes ?? []).map((sh, i) => ({
			name: DOMAIN_SHAPE_LABEL[sh.shape] ?? sh.shape,
			hours: sh.medianHours,
			samples: sh.sampleCount,
			note: sh.example,
			color: ZONE_COLORS[i % ZONE_COLORS.length],
		})),
	);

	// ---------- 2. the pool's buffer ----------

	// Burns per day over the window, counted off the ledger's own timestamps.
	// Nothing is smoothed or back-filled: a day with no burn is a zero because
	// the ledger says zero, not because the chart needed a point.
	const burnDays = $derived.by(() => {
		const days: { iso: string; burned: number }[] = [];
		const start = new Date();
		start.setHours(0, 0, 0, 0);
		for (let i = windowDays - 1; i >= 0; i--) {
			const d = new Date(start.getTime() - i * 86_400_000);
			days.push({ iso: d.toISOString().slice(0, 10), burned: 0 });
		}
		const index = new Map(days.map((d, i) => [d.iso, i]));
		for (const row of domains ?? []) {
			if (!row.burnedAt) continue;
			const key = new Date(row.burnedAt).toISOString().slice(0, 10);
			const at = index.get(key);
			if (at !== undefined) days[at].burned += 1;
		}
		return days;
	});

	const poolRows = $derived.by<HeroRow[]>(() => {
		const p = metrics?.pool;
		return [
			{ label: 'в пуле, свободны', value: `${p?.owned ?? 0}`, color: CHART.ok },
			{ label: 'стоят на машинах', value: `${p?.assigned ?? 0}`, color: CHART.info },
			{ label: 'в покупке', value: `${p?.buying ?? 0}`, color: CHART.violet },
			{
				label: 'сгорает в сутки',
				value: p?.burnedPerDay === null || p?.burnedPerDay === undefined ? 'нет данных' : p.burnedPerDay.toFixed(1),
				color: CHART.bad,
			},
		];
	});

	// The money charts share the metrics' own day axis. Cents are drawn as
	// dollars: a chart whose axis counts cents reads as a different currency.
	const spendLabels = $derived((metrics?.spend.days ?? []).map(formatDay));
	const spendDailyDollars = $derived((metrics?.spend.dailyCents ?? []).map((c) => Math.round(c) / 100));
	const spendDailyAvgDollars = $derived(
		(metrics?.spend.dailyAvg ?? []).map((c) => (c === null ? null : Math.round(c) / 100)),
	);

	const poolHoursLeft = $derived(metrics?.pool.hoursLeft ?? null);

	// ---------- 3. the month's spend ----------

	const spendPct = $derived.by(() => {
		if (!spend || spend.monthlyCapCents <= 0) return null;
		return Math.round((spend.monthCents / spend.monthlyCapCents) * 100);
	});

	const boughtInWindow = $derived.by(() => {
		const cutoff = Date.now() - windowDays * 86_400_000;
		const rows = (domains ?? []).filter(
			(d) => d.source === 'purchased' && d.boughtAt && new Date(d.boughtAt).getTime() >= cutoff,
		);
		const cents = rows.reduce((sum, d) => sum + d.costCents, 0);
		return { count: rows.length, cents, avg: rows.length > 0 ? Math.round(cents / rows.length) : null };
	});

	// ---------- loading ----------

	async function load() {
		try {
			const [ledger, met] = await Promise.all([fetchDomains(), fetchDomainMetrics(range)]);
			domains = ledger.domains;
			spend = ledger.spend;
			metrics = met;
			loadError = null;
		} catch (e) {
			loadError = describeProviderError(e);
		}

		// Both lists are extras: the screen stands without them, so one of
		// them failing does not blank it.
		try {
			exclusions = (await fetchExclusions()).exclusions;
		} catch {
			exclusions = [];
		}
		try {
			accountDomains = spend?.registrar ? (await fetchAccountDomains()).domains : [];
		} catch {
			accountDomains = [];
		}
	}

	function pickRange(w: DomainWindow) {
		if (w === range) return;
		range = w;
		void load();
	}

	function lifetime(d: DomainLedgerRow): string {
		if (!d.assignedAt) return '';
		const end = d.burnedAt ? new Date(d.burnedAt) : new Date();
		const hours = Math.max(0, (end.getTime() - new Date(d.assignedAt).getTime()) / 3_600_000);
		return hours < 48 ? `${Math.round(hours)} ч` : `${Math.round(hours / 24)} дн`;
	}

	function fmtDate(iso?: string): string {
		if (!iso) return '';
		const d = new Date(iso);
		if (Date.now() - d.getTime() < 120_000) return 'только что';
		return `${d.getDate()} ${months[d.getMonth()]}`;
	}

	const STATUS_LABEL: Record<DomainStatus, string> = {
		assigned: 'на машине',
		burned: 'под вопросом',
		dead: 'мёртв',
		owned: 'в пуле',
		buying: 'покупка',
		failed: 'не куплен',
	};

	// ---------- buying ----------

	const blockers = $derived.by(() => {
		const c = checkout;
		if (!c) return [];
		const out: string[] = [];
		if (!c.available) out.push('имя занято или зона не продаётся через api');
		if (c.overCap || !c.withinHubCaps) out.push('покупка выйдет за дневной или месячный бюджет доменов');
		if (c.available && !c.sufficientFunds) out.push(`на кредите ${formatCents(c.balanceCents)}, пополни аккаунт`);
		if (c.available && !c.withinRegistrarLimit) out.push('упрётся в месячный лимит самого аккаунта');
		if (c.available && c.sufficientFunds && c.withinRegistrarLimit && !c.wouldSucceed) {
			out.push('регистратор не продаст');
		}
		return out;
	});

	async function onSuggest() {
		buyError = buyNote = null;
		buyBusy = 'suggest';
		try {
			offers = (await suggestDomains(3)).offers;
			if (offers.length > 0) {
				buyName = offers[0].domain;
				await onCheck(offers[0].domain);
			}
		} catch (e) {
			buyError = describeProviderError(e);
		} finally {
			buyBusy = null;
		}
	}

	async function onCheck(domain = buyName) {
		buyError = buyNote = null;
		buyName = domain.trim();
		if (!buyName) return;
		buyBusy = 'check';
		try {
			checkout = (await checkoutDomain(buyName)).checkout;
		} catch (e) {
			checkout = null;
			buyError = describeProviderError(e);
		} finally {
			buyBusy = null;
		}
	}

	async function onBuy() {
		if (!checkout) return;
		buyError = buyNote = null;
		buyBusy = 'buy';
		try {
			const res = await buyDomain(checkout.domain);
			buyNote = `${res.domain.domain} куплен за ${formatCents(res.domain.costCents)} и лежит в пуле`;
			checkout = null;
			offers = [];
			await load();
		} catch (e) {
			buyError = describeProviderError(e);
		} finally {
			buyBusy = null;
			confirmingBuy = false;
		}
	}

	async function onSeed() {
		buyError = buyNote = null;
		const domain = buyName.trim();
		if (!domain) return;
		buyBusy = 'seed';
		try {
			await seedDomain(domain);
			buyNote = `${domain} добавлен в пул`;
			await load();
		} catch (e) {
			buyError = describeProviderError(e);
		} finally {
			buyBusy = null;
		}
	}

	async function onAddExclusion() {
		if (!newExclDomain.trim()) return;
		exclBusy = true;
		try {
			await excludeDomain(newExclDomain.trim());
			newExclDomain = '';
			addingExcl = false;
			await load();
		} catch (e) {
			loadError = describeProviderError(e);
		} finally {
			exclBusy = false;
		}
	}

	async function onRemoveExclusion(x: DomainExclusion) {
		if (x.builtIn) return;
		try {
			await unexcludeDomain(x.domain);
			await load();
		} catch (e) {
			loadError = describeProviderError(e);
		}
	}

	async function seedAllFree() {
		if (freeAccountDomains.length === 0) return;
		seedingAll = true;
		try {
			for (const d of freeAccountDomains) {
				await seedDomain(d.domain);
			}
			await load();
		} catch (e) {
			loadError = describeProviderError(e);
		} finally {
			seedingAll = false;
		}
	}

	onMount(load);
</script>

<PageShell>
	<div class="entry-scope domains-page">
		<div class="head">
			<h1>домены</h1>
			<div class="right">
				<RangeChips options={WINDOWS} value={range} onpick={pickRange} />
			</div>
		</div>

		{#if loadError}
			<p class="err-banner" role="alert">{loadError}</p>
		{/if}

		{#if domains === null}
			<p class="hint">загружаю</p>
		{:else}
			<!-- ======== 1. what to buy next ======== -->
			<Panel>
				<div class="rankings">
					<div class="ranking">
						<div class="r-title">по зоне</div>
						{#if zoneRanking.length === 0}
							<p class="hint" style="padding: 0">ни одно имя ещё не сгорело</p>
						{:else}
							{#each zoneRanking as r (r.name)}
								<div class="r-row">
									<span class="r-name m">{r.name}</span>
									<div class="r-track">
										<i
											style="width: {r.hours && longestLife > 0
												? Math.max(2, (r.hours / longestLife) * 100)
												: 0}%; background: {r.color}"
										></i>
									</div>
									<span class="r-val m" class:dim={r.hours === null}>{life(r.hours)}</span>
									<span class="r-n">{r.samples}</span>
								</div>
							{/each}
						{/if}
					</div>

					<div class="ranking">
						<div class="r-title">по типу имени</div>
						{#if shapeRanking.length === 0}
							<p class="hint" style="padding: 0">ни одно имя ещё не сгорело</p>
						{:else}
							{#each shapeRanking as r (r.name)}
								<div class="r-row">
									<span class="r-name" title={r.note}>{r.name}</span>
									<div class="r-track">
										<i
											style="width: {r.hours && longestLife > 0
												? Math.max(2, (r.hours / longestLife) * 100)
												: 0}%; background: {r.color}"
										></i>
									</div>
									<span class="r-val m" class:dim={r.hours === null}>{life(r.hours)}</span>
									<span class="r-n">{r.samples}</span>
								</div>
							{/each}
						{/if}
					</div>
				</div>
			</Panel>

			<!-- ======== 2. the buffer that decides whether to buy ======== -->
			<Panel>
				<MetricHero
					value={poolHoursLeft === null ? null : `${Math.round(poolHoursLeft)}`}
					unit=" ч"
					what="хватит доменов в пуле"
					missing={missingOf.get('pool.hoursLeft') ?? 'расход за окно нулевой: считать не от чего'}
					rows={poolRows}
				>
					{#snippet chart()}
						<TimeSeries
							labels={burnDays.map((d) => formatDay(d.iso))}
							series={[
								{
									name: 'сгорело имён',
									color: CHART.bad,
									vals: burnDays.map((d) => d.burned),
									area: true,
								},
							]}
							height="190px"
							emptyNote="за окно не сгорело ни одного имени"
						/>
					{/snippet}
				</MetricHero>
			</Panel>

			<!-- ======== 3. what the names cost ======== -->
			<div class="money">
				<div class="m-card">
					<div class="m-head">
						<span class="m-label">потрачено за месяц</span>
						<span class="m-val m">{formatCents(metrics?.spend.monthSpentCents ?? 0)}</span>
						<span class="m-cap">из {spend ? formatCents(spend.monthlyCapCents) : '—'}</span>
					</div>
					<TimeSeries
						labels={spendLabels}
						series={[{ name: 'потрачено, $', color: CHART.ok, vals: spendDailyDollars, area: true }]}
						height="64px"
						showAxis={false}
						emptyNote="покупок за окно не было"
					/>
				</div>

				<div class="m-card">
					<div class="m-head">
						<span class="m-label">куплено за {windowDays} дн</span>
						<span class="m-val m">{metrics?.spend.boughtCount ?? 0}</span>
						<span class="m-cap">{formatCents(metrics?.spend.boughtCents ?? 0)}</span>
					</div>
					<TimeSeries
						labels={spendLabels}
						series={[{ name: 'куплено имён', color: CHART.info, vals: metrics?.spend.dailyCount ?? [], area: true }]}
						height="64px"
						showAxis={false}
						emptyNote="покупок за окно не было"
					/>
				</div>

				<div class="m-card">
					<div class="m-head">
						<span class="m-label">средняя цена имени</span>
						<span class="m-val m">
							{metrics?.spend.avgCents == null ? 'нет данных' : formatCents(metrics.spend.avgCents)}
						</span>
					</div>
					<TimeSeries
						labels={spendLabels}
						series={[{ name: 'цена имени, $', color: CHART.warn, vals: spendDailyAvgDollars }]}
						height="64px"
						showAxis={false}
						emptyNote="покупок за окно не было"
					/>
				</div>
			</div>

			<!-- ======== 4. buy one into the pool ======== -->
			<Panel title="купить домен в пул">
				<div class="buy">
					<div class="row">
						<input
							class="m"
							bind:value={buyName}
							placeholder="имя.zone"
							spellcheck="false"
							onkeydown={(e) => e.key === 'Enter' && onCheck()}
						/>
						<button class="btn" onclick={() => onCheck()} disabled={buyBusy !== null || !buyName.trim()}>
							{buyBusy === 'check' ? 'проверяю' : 'проверить'}
						</button>
						<button class="btn text" onclick={onSuggest} disabled={buyBusy !== null}>
							{buyBusy === 'suggest' ? 'подбираю' : 'подобрать'}
						</button>
						<button class="btn text" onclick={onSeed} disabled={buyBusy !== null || !buyName.trim()}>
							{buyBusy === 'seed' ? 'добавляю' : 'уже куплен, в пул'}
						</button>
					</div>

					{#if offers.length > 0}
						<div class="offers">
							{#each offers as o (o.domain)}
								<button class="offer" class:on={buyName === o.domain} onclick={() => onCheck(o.domain)}>
									<span class="m">{o.domain}</span>
									<span class="price m">{formatCents(o.costCents)}</span>
								</button>
							{/each}
						</div>
					{/if}

					{#if checkout}
						<div class="checkout">
							<div class="co-line">
								<span class="m nm">{checkout.domain}</span>
								<span class="m">{checkout.available ? formatCents(checkout.costCents) : 'не продаётся'}</span>
								<span class="co-facts">
									кредит <b class="m">{formatCents(checkout.balanceCents)}</b> · за месяц
									<b class="m">{formatCents(checkout.spend.monthCents)}</b> из
									<b class="m">{formatCents(checkout.spend.monthlyCapCents)}</b>
								</span>
								<button
									class="btn prim"
									onclick={() => (confirmingBuy = true)}
									disabled={buyBusy !== null || blockers.length > 0}
								>
									{buyBusy === 'buy' ? 'покупаю' : 'купить'}
								</button>
							</div>
							{#if blockers.length > 0}
								<div class="co-why bad">{blockers.join(' · ')}</div>
							{/if}
						</div>
					{/if}

					{#if buyError}<p class="co-why bad">{buyError}</p>{/if}
					{#if buyNote}<p class="co-why ok">{buyNote}</p>{/if}
				</div>
			</Panel>

			<!-- ======== 5. the names the hub never touches ======== -->
			<Panel title="имена, которые хаб не трогает">
				{#snippet controls()}
					<button class="btn text" onclick={() => (addingExcl = !addingExcl)}>
						{addingExcl ? 'отмена' : '+ добавить'}
					</button>
				{/snippet}

				{#if addingExcl}
					<div class="row excl-add">
						<input
							class="m"
							placeholder="домен или *.поддомен"
							bind:value={newExclDomain}
							disabled={exclBusy}
							onkeydown={(e) => e.key === 'Enter' && onAddExclusion()}
						/>
						<button class="btn prim" onclick={onAddExclusion} disabled={exclBusy || !newExclDomain.trim()}>
							добавить
						</button>
					</div>
				{/if}

				{#if exclusions.length === 0}
					<p class="hint" style="padding: 0">список пуст: хаб может взять любое имя из аккаунта</p>
				{:else}
					<div class="excl">
						{#each exclusions as x (x.domain)}
							<span class="ex-tag m" class:built={x.builtIn}>
								{x.domain}
								{#if !x.builtIn}
									<button class="x" onclick={() => onRemoveExclusion(x)} aria-label="убрать из списка">×</button>
								{/if}
							</span>
						{/each}
					</div>
				{/if}

				{#if freeAccountDomains.length > 0}
					<div class="account-line">
						<span>
							<b class="m">{freeAccountDomains.length}</b>
							{freeAccountDomains.length === 1 ? 'имя куплено' : 'имён куплено'} на
							{spend?.registrar || 'регистраторе'} мимо хаба
						</span>
						<button class="btn" onclick={seedAllFree} disabled={seedingAll}>
							{seedingAll ? 'забираю' : 'забрать в пул'}
						</button>
					</div>
				{/if}
			</Panel>
			<!-- ======== 6. the ledger, last and foldable: it is the longest thing on the screen ======== -->
			<Panel title="журнал" sub="{domains.length} за всё время" flush={ledgerOpen}>
				{#snippet controls()}
					{#if ledgerOpen}
						<button class="chip" class:on={filter === 'all'} onclick={() => (filter = 'all')}>
							все {domains?.length ?? 0}
						</button>
						{#each ['burned', 'dead', 'owned', 'assigned', 'buying', 'failed'] as const as st (st)}
							{#if (counts[st] ?? 0) > 0}
								<button class="chip" class:on={filter === st} onclick={() => (filter = st)}>
									{STATUS_LABEL[st]}
									{counts[st]}
								</button>
							{/if}
						{/each}
					{/if}
					<button class="chip" onclick={() => (ledgerOpen = !ledgerOpen)}>
						{ledgerOpen ? 'свернуть' : 'развернуть'}
					</button>
				{/snippet}

				{#if !ledgerOpen}
					<!-- folded: the head carries the count and the button -->
				{:else if shownLedger.length === 0}
					<p class="hint">
						{domains.length === 0 ? 'хаб не купил и не принял ни одного имени' : 'в этой выборке пусто'}
					</p>
				{:else}
					<table class="ledger">
						<thead>
							<tr>
								<th>домен</th>
								<th>статус</th>
								<th>машина</th>
								<th>прожил</th>
								<th class="num">цена</th>
								<th>заказ</th>
								<th>заведён</th>
							</tr>
						</thead>
						<tbody>
							{#each shownLedger as d (d.domain)}
								<tr class={d.status}>
									<td class="dm m">{d.domain}</td>
									<td><span class="st st-{d.status}">{STATUS_LABEL[d.status]}</span></td>
									<td class="m">{d.box ?? ''}</td>
									<td class="m">{lifetime(d)}</td>
									<td class="num m">{d.costCents > 0 ? formatCents(d.costCents) : ''}</td>
									<td class="m meta">
										{d.orderId ?? ''}{#if d.source === 'seeded'}
											· руками{/if}
									</td>
									<td class="meta">{fmtDate(d.createdAt)}</td>
								</tr>
								{#if d.error}
									<tr class="failed">
										<td colspan="7" class="why">{d.error}</td>
									</tr>
								{/if}
							{/each}
						</tbody>
					</table>
				{/if}
			</Panel>
		{/if}
	</div>
</PageShell>

<!-- Buy confirmation: the money leaves the account on this click. -->
{#if confirmingBuy && checkout}
	<div class="scrim" onclick={() => buyBusy === null && (confirmingBuy = false)} role="presentation"></div>
	<div class="modal entry-scope" role="dialog" aria-modal="true" aria-labelledby="buy-title">
		<h2 id="buy-title">купить {checkout.domain}?</h2>
		<p class="modal-body">
			{formatCents(checkout.costCents)} спишутся с кредита регистратора. если цена выросла выше потолка, хаб переспросит и
			покупки не будет. имя ляжет в пул и уйдёт на следующую машину.
		</p>
		<div class="modal-actions">
			<button class="btn text" onclick={() => (confirmingBuy = false)} disabled={buyBusy !== null}>отмена</button>
			<button class="btn prim" onclick={onBuy} disabled={buyBusy !== null}>
				{buyBusy === 'buy' ? 'покупаю' : 'купить'}
			</button>
		</div>
	</div>
{/if}

<style>
	/* ---------- controls shared by the panels ---------- */

	.chip {
		padding: 4px 11px;
		border-radius: 6px;
		font-size: 11px;
		color: var(--text-2);
		border: 1px solid var(--border);
		white-space: nowrap;
		cursor: pointer;
		background: none;
	}

	.chip:hover {
		background: var(--surface);
	}

	.chip.on {
		color: var(--text);
		background: var(--primary-subtle);
		border-color: oklch(0.75 0.14 350 / 0.35);
	}

	.btn {
		padding: 5px 12px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--text);
		font-size: 12px;
		cursor: pointer;
	}

	.btn:hover:not(:disabled) {
		background: var(--surface-hover);
	}

	.btn:disabled {
		opacity: 0.45;
		cursor: default;
	}

	.btn.text {
		background: none;
		border-color: transparent;
		color: var(--text-2);
	}

	.btn.prim {
		background: var(--primary);
		border-color: var(--primary);
		color: oklch(0.16 0 0);
		font-weight: 600;
	}

	/* ---------- what survives longer ---------- */

	.rankings {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 28px;
	}

	.r-title {
		font-size: 11px;
		color: var(--text-3);
		margin-bottom: 10px;
	}

	.r-row {
		display: grid;
		grid-template-columns: 120px 1fr 64px 28px;
		align-items: center;
		gap: 10px;
		padding: 4px 0;
		font-size: 12px;
		color: var(--text-2);
	}

	.r-name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--text);
	}

	.r-track {
		height: 6px;
		border-radius: 3px;
		background: var(--inset);
		overflow: hidden;
	}

	.r-track i {
		display: block;
		height: 100%;
		border-radius: 3px;
	}

	.r-val {
		text-align: right;
		color: var(--text);
	}

	.r-val.dim {
		color: var(--text-3);
		font-size: 11px;
	}

	.r-n {
		text-align: right;
		font-size: 11px;
		color: var(--text-3);
	}

	@media (max-width: 1000px) {
		.rankings {
			grid-template-columns: 1fr;
		}
	}

	/* ---------- what the names cost ---------- */

	.money {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 12px;
		margin-bottom: 16px;
	}

	.m-card {
		background: var(--card-bg);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		padding: 12px 14px 8px;
		min-width: 0;
	}

	.m-head {
		display: flex;
		align-items: baseline;
		gap: 8px;
		margin-bottom: 4px;
	}

	.m-label {
		font-size: 11px;
		color: var(--text-3);
	}

	.m-val {
		margin-left: auto;
		font-size: 17px;
		font-weight: 600;
		color: var(--text);
	}

	.m-cap {
		font-size: 11px;
		color: var(--text-3);
	}

	@media (max-width: 1000px) {
		.money {
			grid-template-columns: 1fr;
		}
	}

	/* ---------- buying ---------- */

	.row {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	input {
		flex: 1;
		min-width: 0;
		padding: 6px 10px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--inset);
		color: var(--text);
		font-size: 12px;
	}

	input:focus {
		outline: none;
		border-color: var(--primary);
	}

	.offers {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		margin-top: 10px;
	}

	.offer {
		display: flex;
		align-items: baseline;
		gap: 8px;
		padding: 5px 10px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--inset);
		color: var(--text-2);
		font-size: 12px;
		cursor: pointer;
	}

	.offer.on {
		border-color: var(--primary);
		color: var(--text);
	}

	.offer .price {
		color: var(--text-3);
		font-size: 11px;
	}

	.checkout {
		margin-top: 12px;
		padding: 10px 12px;
		border-radius: 8px;
		background: var(--inset);
		border: 1px solid var(--border-subtle);
	}

	.co-line {
		display: flex;
		align-items: center;
		gap: 12px;
	}

	.co-line .nm {
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
	}

	.co-facts {
		margin-left: auto;
		font-size: 11px;
		color: var(--text-3);
	}

	.co-facts b {
		color: var(--text-2);
		font-weight: 500;
	}

	.co-why {
		margin: 8px 0 0;
		font-size: 11px;
	}

	.co-why.bad {
		color: var(--bad);
	}

	.co-why.ok {
		color: var(--ok);
	}

	/* ---------- ledger ---------- */

	.ledger {
		width: 100%;
		border-collapse: collapse;
		font-size: 12px;
	}

	.ledger th {
		padding: 9px 16px;
		text-align: left;
		font-size: 10px;
		font-weight: 500;
		color: var(--text-3);
		border-bottom: 1px solid var(--border-subtle);
		text-transform: lowercase;
	}

	.ledger th.num,
	.ledger td.num {
		text-align: right;
	}

	.ledger td {
		padding: 8px 16px;
		border-bottom: 1px solid var(--border-subtle);
		color: var(--text-2);
		white-space: nowrap;
	}

	.ledger tbody tr:last-child td {
		border-bottom: none;
	}

	.ledger td.dm {
		color: var(--text);
	}

	.ledger td.meta {
		color: var(--text-3);
		font-size: 11px;
	}

	.ledger td.why {
		color: var(--bad);
		font-size: 11px;
		white-space: normal;
	}

	.ledger tr.burned td.dm,
	.ledger tr.failed td.dm {
		color: var(--text-2);
	}

	.st {
		display: inline-block;
		padding: 2px 7px;
		border-radius: 4px;
		font-size: 10px;
		background: var(--surface);
		color: var(--text-2);
	}

	.st-owned {
		color: var(--ok);
		background: oklch(0.72 0.15 155 / 0.12);
	}

	.st-assigned {
		color: var(--c-hyst);
		background: oklch(0.72 0.12 200 / 0.12);
	}

	.st-burned {
		color: var(--bad);
		background: oklch(0.65 0.2 25 / 0.12);
	}

	.st-buying {
		color: var(--warn);
		background: oklch(0.78 0.12 85 / 0.12);
	}

	.st-failed,
	.st-dead {
		color: var(--text-3);
	}

	/* ---------- exclusions ---------- */

	.excl-add {
		margin-bottom: 12px;
	}

	.excl {
		display: flex;
		flex-wrap: wrap;
		gap: 7px;
	}

	.ex-tag {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 3px 8px;
		border-radius: 6px;
		background: var(--inset);
		border: 1px solid var(--border-subtle);
		font-size: 11px;
		color: var(--text-2);
	}

	.ex-tag.built {
		color: var(--text-3);
	}

	.ex-tag .x {
		border: none;
		background: none;
		color: var(--text-3);
		cursor: pointer;
		font-size: 13px;
		line-height: 1;
		padding: 0;
	}

	.ex-tag .x:hover {
		color: var(--bad);
	}

	.account-line {
		display: flex;
		align-items: center;
		gap: 12px;
		margin-top: 14px;
		padding-top: 12px;
		border-top: 1px solid var(--border-subtle);
		font-size: 12px;
		color: var(--text-2);
	}

	.account-line button {
		margin-left: auto;
	}

	/* ---------- modal ---------- */

	.scrim {
		position: fixed;
		inset: 0;
		background: oklch(0.1 0 0 / 0.6);
		z-index: 40;
	}

	.modal {
		position: fixed;
		top: 50%;
		left: 50%;
		transform: translate(-50%, -50%);
		z-index: 41;
		width: min(440px, calc(100vw - 32px));
		padding: 20px;
		border-radius: 10px;
		background: var(--card-bg);
		border: 1px solid var(--border);
	}

	.modal h2 {
		margin: 0 0 8px;
		font-size: 15px;
		font-weight: 600;
	}

	.modal-body {
		margin: 0 0 16px;
		font-size: 12px;
		color: var(--text-2);
	}

	.modal-actions {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
	}
</style>
