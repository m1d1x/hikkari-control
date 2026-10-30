<script lang="ts">
	import { onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import Panel from '$components/entry/ui/Panel.svelte';
	import MetricHero from '$components/entry/ui/MetricHero.svelte';
	import RangeChips from '$components/entry/ui/RangeChips.svelte';
	import TimeSeries from '$components/entry/ui/TimeSeries.svelte';
	import OrderBoxCard from '$components/entry/OrderBoxCard.svelte';
	import JobPanel from '$components/entry/JobPanel.svelte';
	import Knob from '$components/entry/ui/Knob.svelte';
	import Stepper from '$components/entry/ui/Stepper.svelte';
	import type { HeroRow } from '$components/entry/ui/MetricHero.svelte';
	import { CHART } from '$components/entry/ui/colors';
	import {
		fetchPressure,
		fetchPool,
		fetchBenchPolicy,
		saveBenchPolicy,
		createVantage,
		saveVantage,
		retireVantage,
		reissueVantageToken,
		describeEntryError,
		type CensorRange,
		type EntryPressure,
		type PressureVantage,
		type VantageIssued,
		type BenchPolicy,
		type PoolBox,
		type Job,
	} from '$lib/api/entry';

	// The prober screen (roadmap 42 К3′).
	//
	// It used to be a wall: three checks, a verdict and a ribbon for every
	// point of entry, on the assumption that someone reads them. At a
	// thousand relays nobody does, and nobody should — the fleet's own screen
	// already lists the relays, and auditing them by hand is not a job that
	// scales.
	//
	// So the screen is three things: how hard the censor is pushing, who is
	// looking, and the moments the autopilot stood down. The thresholds it
	// evicts by moved to the autopilot screen with its other knobs (П9.10). It
	// briefly also carried a "share of evictions that were a mistake": an
	// honest measurement of a question nobody has — an evicted relay costs
	// nothing to replace at hourly billing, so there is no trade-off to tune
	// against it. Every figure comes from GET /entry/pressure.

	const RANGES: readonly { key: CensorRange; label: string }[] = [
		{ key: '15m', label: '15 м' },
		{ key: '1h', label: '1 ч' },
		{ key: '6h', label: '6 ч' },
		{ key: '24h', label: '24 ч' },
	];

	// The causes as the fuse writes them, in the console's words.
	const CAUSE_LABEL: Record<string, string> = {
		domain_blocked: 'домен заблокирован',
		ip_banned: 'ip забанен',
		fingerprint: 'отпечаток спалили',
		ports_banned: 'порты забанены',
		destination: 'назначение заблокировано',
		provider_down: 'провайдер лёг',
	};

	// A point's last word, as the prober classifies a row.
	const VERDICT_LABEL: Record<string, string> = {
		...CAUSE_LABEL,
		clean: 'чисто',
		reality_wins: 'reality прошёл',
		network_down: 'у точки нет сети',
	};

	let range = $state<CensorRange>('6h');
	let pressure = $state<EntryPressure | null>(null);
	let loadError = $state<string | null>(null);

	let bench = $state<BenchPolicy | null>(null);
	let benchError = $state<string | null>(null);
	let benchSaving = $state(false);
	let benchSaved = $state(false);

	// ---------- observation point slots (К3′ / П9.12) ----------

	let boxes = $state<PoolBox[]>([]);
	let selectedSlot = $state<string | null>(null);
	let replaceTab = $state<'pool' | 'external' | 'order'>('pool');
	let editTitle = $state('');
	let editBox = $state('');
	let editHost = $state('');
	let editProvider = $state('');
	let orderJobId = $state<string | null>(null);
	let proberJobId = $state<string | null>(null);

	let showCreateSlot = $state(false);
	let newSlotWhere = $state<'hub' | 'external'>('hub');
	let newSlotTitle = $state('');
	let newSlotBox = $state('');
	let newSlotHost = $state('');
	let newSlotProvider = $state('');

	let vActionBusy = $state(false);
	let vActionError = $state<string | null>(null);

	// A standalone slot's token, shown once: the hub keeps only its hash, so
	// it lives in this variable until the owner closes the panel and nowhere
	// else (not in storage, not in the URL).
	let issued = $state<{ slot: string; token: string; env: string } | null>(null);
	let copied = $state<'token' | 'command' | null>(null);

	const liveBoxes = $derived(boxes.filter((b) => !b.retiredAt));
	// A machine with relays cannot be an observation point (owner's decision
	// 27.10): the hub refuses it, so it is not offered.
	const hubBoxes = $derived(liveBoxes.filter((b) => (b.relays ?? []).length === 0));
	const selectedVantage = $derived(
		(pressure?.vantages ?? []).find((v) => v.region === selectedSlot) ?? null,
	);

	function slotName(region: string): string {
		return `слот ${region.toUpperCase()}`;
	}

	function whereLabel(v: PressureVantage): string {
		if (v.box) return `машина хаба ${v.box}`;
		if (v.host || v.unreadable) return 'внешняя машина';
		return 'нет машины';
	}

	function verdictLabel(v: string | undefined): string {
		if (!v) return 'нет данных';
		return VERDICT_LABEL[v] ?? v;
	}

	function toggleSlot(v: PressureVantage) {
		vActionError = null;
		showCreateSlot = false;
		if (selectedSlot === v.region) {
			selectedSlot = null;
			return;
		}
		selectedSlot = v.region;
		editTitle = v.title ?? '';
		editBox = v.box ?? '';
		editHost = v.box ? '' : (v.host ?? '');
		editProvider = v.box ? '' : (v.provider ?? '');
		replaceTab = v.box || !v.host ? 'pool' : 'external';
	}

	/** What a token-issuing answer leaves on the screen. */
	function takeIssued(a: VantageIssued) {
		proberJobId = a.jobId ?? null;
		issued = a.token ? { slot: a.slot, token: a.token, env: a.env ?? '' } : null;
		copied = null;
		if (a.deployError) vActionError = `пробник не развёрнут: ${a.deployError}`;
	}

	/** The env as one paste for a root shell on the VPS. */
	function installCommand(env: string): string {
		return [
			"install -d -m 700 /etc/nekko-entry-prober && umask 077 && cat > /etc/nekko-entry-prober/prober.env <<'EOF'",
			env.trimEnd(),
			'EOF',
			'systemctl restart nekko-entry-prober',
		].join('\n');
	}

	async function copy(text: string, what: 'token' | 'command') {
		try {
			await navigator.clipboard.writeText(text);
			copied = what;
			setTimeout(() => (copied = null), 1500);
		} catch {
			vActionError = 'буфер обмена недоступен, выдели и скопируй руками';
		}
	}

	function formatTimestamp(unixSec: number): string {
		if (!unixSec || unixSec <= 0) return 'нет данных';
		return new Date(unixSec * 1000).toLocaleString('ru-RU', {
			day: '2-digit',
			month: '2-digit',
			hour: '2-digit',
			minute: '2-digit',
		});
	}

	function formatIsoDate(iso?: string): string {
		if (!iso) return 'нет данных';
		const d = new Date(iso);
		if (Number.isNaN(d.getTime())) return 'нет данных';
		return d.toLocaleString('ru-RU', {
			day: '2-digit',
			month: '2-digit',
			year: 'numeric',
			hour: '2-digit',
			minute: '2-digit',
		});
	}

	async function loadBoxes() {
		try {
			const res = await fetchPool();
			boxes = res.boxes ?? [];
		} catch {
			/* pool list is optional until a slot is edited */
		}
	}

	async function submitCreateSlot(e: SubmitEvent) {
		e.preventDefault();
		vActionBusy = true;
		vActionError = null;
		try {
			const created = await createVantage(
				newSlotWhere === 'hub'
					? { title: newSlotTitle.trim(), box: newSlotBox }
					: { title: newSlotTitle.trim(), host: newSlotHost.trim(), provider: newSlotProvider.trim() },
			);
			newSlotTitle = '';
			newSlotBox = '';
			newSlotHost = '';
			newSlotProvider = '';
			showCreateSlot = false;
			selectedSlot = created.slot;
			editTitle = created.title ?? '';
			editBox = created.box ?? '';
			editHost = created.box ? '' : (created.host ?? '');
			editProvider = created.box ? '' : created.provider;
			replaceTab = created.box ? 'pool' : 'external';
			takeIssued(created);
			await load();
		} catch (err) {
			vActionError = describeEntryError(err);
		} finally {
			vActionBusy = false;
		}
	}

	async function submitSaveSlot(e: SubmitEvent) {
		e.preventDefault();
		if (!selectedSlot) return;
		vActionBusy = true;
		vActionError = null;
		try {
			const saved = await saveVantage(
				selectedSlot,
				replaceTab === 'external'
					? { title: editTitle.trim(), host: editHost.trim(), provider: editProvider.trim() }
					: { title: editTitle.trim(), box: editBox },
			);
			takeIssued(saved);
			await load();
		} catch (err) {
			vActionError = describeEntryError(err);
		} finally {
			vActionBusy = false;
		}
	}

	async function handleReissue(slot: string) {
		vActionBusy = true;
		vActionError = null;
		try {
			takeIssued(await reissueVantageToken(slot));
			await load();
		} catch (err) {
			vActionError = describeEntryError(err);
		} finally {
			vActionBusy = false;
		}
	}

	async function handleRetireSlot(slot: string) {
		vActionBusy = true;
		vActionError = null;
		try {
			await retireVantage(slot);
			if (selectedSlot === slot) selectedSlot = null;
			if (issued?.slot === slot) issued = null;
			proberJobId = null;
			await load();
		} catch (err) {
			vActionError = describeEntryError(err);
		} finally {
			vActionBusy = false;
		}
	}

	async function handleOrderFinished(job: Job) {
		await loadBoxes();
		if (job.status === 'done' && job.box && selectedSlot) {
			editBox = job.box;
			replaceTab = 'pool';
			try {
				const saved = await saveVantage(selectedSlot, {
					title: editTitle.trim(),
					box: job.box,
				});
				takeIssued(saved);
				await load();
			} catch (err) {
				vActionError = describeEntryError(err);
			}
		}
	}

	const missingOf = $derived(new Map((pressure?.missing ?? []).map((m) => [m.figure, m.why])));

	// One colour per vantage, held across the rows and the chart so a line and
	// the row above it are plainly the same thing.
	const VANTAGE_COLORS = [CHART.info, CHART.violet, CHART.ok, CHART.pink, CHART.warn];

	/** A prober silent for more than ten minutes is not a quiet censor. */
	function isStale(lastSeen: number): boolean {
		return Date.now() / 1000 - lastSeen > 600;
	}

	const labels = $derived(
		(pressure?.points ?? []).map((p) =>
			new Date(p.ts * 1000).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' }),
		),
	);

	/** A bucket nothing probed is a gap, not a bucket with zero events. */
	function vals(pick: (p: NonNullable<EntryPressure['points']>[number]) => number): (number | null)[] {
		return (pressure?.points ?? []).map((p) => (p.probes === 0 ? null : pick(p)));
	}

	// ---------- how hard the censor pushes ----------

	const banRows = $derived.by<HeroRow[]>(() => {
		const r = pressure?.rate;
		const fmt = (v: number | undefined) => (v === undefined ? 'нет данных' : `${v.toFixed(1)} /ч`);
		return [
			{ label: 'домен заблокирован', value: fmt(r?.domainBlocked), color: CHART.warn },
			{ label: 'ip забанен', value: fmt(r?.ipBanned), color: CHART.bad },
			{ label: 'отпечаток спалили', value: fmt(r?.fingerprint), color: CHART.violet },
			{ label: 'всего за окно', value: `${pressure?.bans.total ?? 0}`, separated: true, tone: 'dim' as const },
		];
	});

	/** A pause stands where its bucket is, so the fuse lines up with the tempo. */
	const banMarks = $derived.by(() => {
		const pts = pressure?.points ?? [];
		if (pts.length === 0) return [];
		return (pressure?.waves ?? []).map((w) => {
			let nearest = 0;
			for (let i = 0; i < pts.length; i++) {
				if (Math.abs(pts[i].ts - w.pausedAt) < Math.abs(pts[nearest].ts - w.pausedAt)) nearest = i;
			}
			return { at: nearest, label: 'пауза' };
		});
	});

	// ---------- the canary's thresholds ----------

	function minutes(sec: number): string {
		return sec % 60 === 0 ? `${sec / 60}` : (sec / 60).toFixed(1);
	}

	// Each knob saves itself. The patch names only what moved, so one stepper
	// cannot undo another, and a refusal is shown as the hub worded it.
	async function saveBenchKnob(patch: Parameters<typeof saveBenchPolicy>[0]) {
		benchSaving = true;
		benchError = null;
		try {
			bench = await saveBenchPolicy(patch);
			benchSaved = true;
			setTimeout(() => (benchSaved = false), 2000);
		} catch (e) {
			benchError = describeEntryError(e);
			// The row is the truth and the server may have refused on a bound
			// of its own, so the screen re-reads rather than keeping the value
			// a person typed.
			try {
				bench = await fetchBenchPolicy();
			} catch {
				/* the error above is what the screen shows */
			}
		} finally {
			benchSaving = false;
		}
	}

	async function load() {
		try {
			pressure = await fetchPressure(range);
			loadError = null;
		} catch (e) {
			loadError = describeEntryError(e);
		}
	}

	async function loadBench() {
		try {
			bench = await fetchBenchPolicy();
			benchError = null;
		} catch (e) {
			benchError = describeEntryError(e);
		}
	}

	function pickRange(r: CensorRange) {
		if (r === range) return;
		range = r;
		void load();
	}

	onMount(() => {
		void load();
		void loadBench();
		void loadBoxes();
	});
</script>

<PageShell>
	<div class="entry-scope censor-page">
		<div class="head">
			<h1>пробник</h1>
			<div class="right">
				<RangeChips options={RANGES} value={range} onpick={pickRange} />
			</div>
		</div>

		{#if loadError}
			<p class="err-banner" role="alert">{loadError}</p>
		{:else if !pressure}
			<p class="hint">загружаю</p>
		{:else}
			<!-- ======== 1. how hard the censor is pushing ======== -->
			<Panel>
				<MetricHero
					value={pressure.rate.total === undefined ? null : pressure.rate.total.toFixed(1)}
					unit=" бана/ч"
					what="темп банов"
					missing={missingOf.get('rate.total') ?? 'за окно не было ни одной проверки'}
					rows={banRows}
				>
					{#snippet chart()}
						<TimeSeries
							{labels}
							series={[
								{ name: 'домен', color: CHART.warn, vals: vals((p) => p.domainBlocked), area: true },
								{ name: 'ip', color: CHART.bad, vals: vals((p) => p.ipBanned), area: true },
								{ name: 'отпечаток', color: CHART.violet, vals: vals((p) => p.fingerprint), area: true },
							]}
							marks={banMarks}
							height="190px"
							emptyNote="за окно пробник не отчитался ни разу"
						/>
					{/snippet}
				</MetricHero>
			</Panel>
			<!-- ======== 2. who is doing the looking ======== -->
			<Panel title="точки наблюдения" sub="всё на экране увидели они">
				{#snippet controls()}
					<button
						type="button"
						class="vaction-btn"
						onclick={() => {
							showCreateSlot = !showCreateSlot;
							if (showCreateSlot) selectedSlot = null;
							vActionError = null;
						}}
					>
						{showCreateSlot ? 'отмена' : '+ слот'}
					</button>
				{/snippet}

				{#if issued}
					<div class="vtoken" role="status">
						<div class="vtoken-head">
							<span class="vdetail-slot m">{slotName(issued.slot)}</span>
							<span class="vtoken-note">токен показан один раз: хаб хранит только хеш</span>
						</div>
						<div class="vtoken-row">
							<code class="vtoken-val m">{issued.token}</code>
							<button type="button" class="vbtn" onclick={() => issued && copy(issued.token, 'token')}>
								{copied === 'token' ? 'скопировано' : 'скопировать'}
							</button>
						</div>
						{#if issued.env}
							<span class="vlabel">на vps под root, пробник уже стоит</span>
							<pre class="vtoken-cmd m">{installCommand(issued.env)}</pre>
							<div class="vform-actions">
								<button
									type="button"
									class="vbtn"
									onclick={() => issued && copy(installCommand(issued.env), 'command')}
								>
									{copied === 'command' ? 'скопировано' : 'скопировать команду'}
								</button>
								<button type="button" class="vbtn primary" onclick={() => (issued = null)}>сохранил</button>
							</div>
						{:else}
							<span class="vlabel">в prober.env: PROBE_INGEST_TOKEN=токен</span>
							<div class="vform-actions">
								<button type="button" class="vbtn primary" onclick={() => (issued = null)}>сохранил</button>
							</div>
						{/if}
					</div>
				{/if}

				{#if showCreateSlot}
					<form class="vform" onsubmit={submitCreateSlot}>
						<div class="vtabs">
							<button
								type="button"
								class="vtab"
								class:active={newSlotWhere === 'hub'}
								onclick={() => (newSlotWhere = 'hub')}>машина хаба</button
							>
							<button
								type="button"
								class="vtab"
								class:active={newSlotWhere === 'external'}
								onclick={() => (newSlotWhere = 'external')}>внешняя машина</button
							>
						</div>
						<div class="vform-grid">
							{#if newSlotWhere === 'hub'}
								<label class="vfield">
									<span class="vlabel">машина без релеев</span>
									<select class="vinput m" bind:value={newSlotBox}>
										<option value="">выбрать машину</option>
										{#each hubBoxes as b (b.name)}
											<option value={b.name}>
												{b.name} · {b.address ?? 'нет адреса'} · {b.provider} ({b.location})
											</option>
										{/each}
									</select>
								</label>
							{:else}
								<label class="vfield">
									<span class="vlabel">ip или домен</span>
									<input class="vinput m" type="text" placeholder="198.51.100.7" bind:value={newSlotHost} />
								</label>
								<label class="vfield">
									<span class="vlabel">провайдер</span>
									<input class="vinput" type="text" placeholder="selectel" bind:value={newSlotProvider} />
								</label>
							{/if}
							<label class="vfield">
								<span class="vlabel">название</span>
								<input
									class="vinput"
									type="text"
									placeholder="санкт-петербург"
									bind:value={newSlotTitle}
								/>
							</label>
						</div>
						<p class="hint" style="padding: 0">
							{newSlotWhere === 'hub'
								? 'хаб сам развернёт пробник и положит токен на машину'
								: 'хаб выдаст токен один раз, его надо положить на vps'}
						</p>

						<div class="vform-actions">
							<button
								type="submit"
								class="vbtn primary"
								disabled={vActionBusy || (newSlotWhere === 'hub' ? !newSlotBox : !newSlotHost.trim())}
							>
								{newSlotWhere === 'hub' ? 'завести и развернуть' : 'завести и выдать токен'}
							</button>
						</div>
					</form>
				{/if}

				{#if pressure.vantages.length === 0}
					<p class="hint" style="padding: 0">за окно не отчиталась ни одна точка: экран читать нечем</p>
				{:else}
					<div class="vcards">
						{#each pressure.vantages as v, i (v.region)}
							<button
								type="button"
								class="vcard"
								class:active={selectedSlot === v.region}
								onclick={() => toggleSlot(v)}
							>
								<div class="vcard-top">
									<span class="vdot" style="background: {VANTAGE_COLORS[i % VANTAGE_COLORS.length]}"></span>
									<span class="vname m">{slotName(v.region)}</span>
									{#if v.title}
										<span class="vtitle">{v.title}</span>
									{/if}
									{#if v.silent || isStale(v.lastSeen)}
										<span class="vstale">молчит</span>
									{/if}
								</div>
								<div class="vcard-machine">
									{#if v.unreadable}
										<span class="vmachine-empty">ключ не открыл хост</span>
									{:else if v.box || v.host}
										{#if v.box}<span class="vbox m">{v.box}</span>{/if}
										{#if v.host}<span class="vhost m">{v.host}</span>{/if}
										{#if v.provider}<span class="vprov">{v.provider}</span>{/if}
									{:else}
										<span class="vmachine-empty">нет машины</span>
									{/if}
								</div>
								<div class="vcard-stats m">
									<span>{v.probes} проб</span>
									<span>·</span>
									<span>{v.lastSeen > 0 ? formatTimestamp(v.lastSeen) : 'нет данных'}</span>
								</div>
							</button>
						{/each}
					</div>

					{#if selectedVantage}
						<div class="vdetail">
							<div class="vdetail-head">
								<div class="vdetail-meta">
									<span class="vdetail-slot m">{slotName(selectedVantage.region)}</span>
									{#if selectedVantage.title}
										<span class="vdetail-title">{selectedVantage.title}</span>
									{/if}
									{#if selectedVantage.silent || isStale(selectedVantage.lastSeen)}
										<span class="vstale">молчит</span>
									{/if}
								</div>
								<div class="vdetail-actions">
									{#if selectedVantage.box || selectedVantage.host || selectedVantage.unreadable}
										<button
											type="button"
											class="vbtn"
											disabled={vActionBusy}
											onclick={() => handleReissue(selectedVantage.region)}
										>
											{selectedVantage.box ? 'развернуть пробник заново' : 'перевыпустить токен'}
										</button>
									{/if}
									<button
										type="button"
										class="vbtn danger"
										disabled={vActionBusy}
										onclick={() => handleRetireSlot(selectedVantage.region)}
									>
										снять слот
									</button>
								</div>
							</div>
							<p class="hint" style="padding: 0">
								{selectedVantage.box
									? 'новый токен ляжет на машину, старый сразу перестанет работать'
									: 'старый токен сразу перестанет работать, новый покажем один раз'}
							</p>

							<div class="vkv-grid">
								<div class="vkv">
									<span class="vlabel">где стоит</span>
									<span class="m">{whereLabel(selectedVantage)}</span>
								</div>
								<div class="vkv">
									<span class="vlabel">пробник</span>
									<span class:vstale-text={selectedVantage.silent || isStale(selectedVantage.lastSeen)}>
										{selectedVantage.lastSeen <= 0
											? 'ни разу не отчитался'
											: selectedVantage.silent || isStale(selectedVantage.lastSeen)
												? 'молчит'
												: 'жив'}
									</span>
								</div>
								<div class="vkv">
									<span class="vlabel">последний вердикт</span>
									<span>{verdictLabel(selectedVantage.lastVerdict)}</span>
								</div>
								<div class="vkv">
									<span class="vlabel">хост</span>
									<span class="m">
										{selectedVantage.unreadable
											? 'ключ не открыл хост'
											: selectedVantage.host || 'нет данных'}
									</span>
								</div>
								<div class="vkv">
									<span class="vlabel">провайдер</span>
									<span>{selectedVantage.provider || 'нет данных'}</span>
								</div>
								<div class="vkv">
									<span class="vlabel">заведена</span>
									<span class="m">{formatIsoDate(selectedVantage.createdAt)}</span>
								</div>
								<div class="vkv">
									<span class="vlabel">последний отчёт</span>
									<span class="m">{formatTimestamp(selectedVantage.lastSeen)}</span>
								</div>
								<div class="vkv">
									<span class="vlabel">проб за окно</span>
									<span class="m">{selectedVantage.probes} ({selectedVantage.relays} релеев)</span>
								</div>
							</div>

							<div class="vtabs">
								<button
									type="button"
									class="vtab"
									class:active={replaceTab === 'pool'}
									onclick={() => (replaceTab = 'pool')}>машина хаба</button
								>
								<button
									type="button"
									class="vtab"
									class:active={replaceTab === 'external'}
									onclick={() => (replaceTab = 'external')}>внешняя машина</button
								>
								<button
									type="button"
									class="vtab"
									class:active={replaceTab === 'order'}
									onclick={() => (replaceTab = 'order')}>заказать новую</button
								>
							</div>

							{#if replaceTab === 'pool'}
								<form class="vform" onsubmit={submitSaveSlot}>
									<div class="vform-grid">
										<label class="vfield">
											<span class="vlabel">машина без релеев</span>
											<select class="vinput m" bind:value={editBox}>
												<option value="">выбрать машину</option>
												{#each hubBoxes as b (b.name)}
													<option value={b.name}>
														{b.name} · {b.address ?? 'нет адреса'} · {b.provider} ({b.location})
													</option>
												{/each}
											</select>
										</label>
										<label class="vfield">
											<span class="vlabel">название</span>
											<input class="vinput" type="text" bind:value={editTitle} />
										</label>
									</div>
									<div class="vform-actions">
										<button type="submit" class="vbtn primary" disabled={vActionBusy || !editBox}>
											поставить в слот
										</button>
									</div>
								</form>
							{:else if replaceTab === 'external'}
								<form class="vform" onsubmit={submitSaveSlot}>
									<div class="vform-grid">
										<label class="vfield">
											<span class="vlabel">ip или домен</span>
											<input class="vinput m" type="text" bind:value={editHost} />
										</label>
										<label class="vfield">
											<span class="vlabel">провайдер</span>
											<input class="vinput" type="text" bind:value={editProvider} />
										</label>
										<label class="vfield">
											<span class="vlabel">название</span>
											<input class="vinput" type="text" bind:value={editTitle} />
										</label>
									</div>
									<div class="vform-actions">
										<button type="submit" class="vbtn primary" disabled={vActionBusy || !editHost.trim()}>
											поставить в слот
										</button>
									</div>
								</form>
							{:else}
								<div class="vorder-wrap">
									<OrderBoxCard onstarted={(id) => (orderJobId = id)} />
									{#if orderJobId}
										<JobPanel jobId={orderJobId} onfinished={handleOrderFinished} />
									{/if}
								</div>
							{/if}

							{#if proberJobId}
								<JobPanel jobId={proberJobId} onfinished={() => void load()} />
							{/if}
						</div>
					{/if}

					{#if vActionError}
						<p class="err-banner" role="alert">{vActionError}</p>
					{/if}

					<div class="vchart">
						<TimeSeries
							{labels}
							series={pressure.vantages.map((v, i) => ({
								name: `${slotName(v.region)}${v.title ? ` (${v.title})` : ''}, проб`,
								color: VANTAGE_COLORS[i % VANTAGE_COLORS.length],
								vals: v.perBucket,
								area: true,
							}))}
							height="130px"
							emptyNote="проб за окно не было"
						/>
					</div>
				{/if}
			</Panel>

			<!-- ======== 3. what the canary judges a build by ======== -->
			<Panel title="пороги канарейки" sub="по этим числам новая сборка релея раскатывается или откатывается" flush>
				{#snippet controls()}
					{#if benchSaved}<span class="saved">сохранено</span>{/if}
				{/snippet}

				{#if !bench}
					<p class="hint pad">{benchError ?? 'загружаю'}</p>
				{:else}
					<Knob
						label="пар прогонов на один вердикт"
						hint="одна пара не отличает сборку медленнее от минуты медленнее: на этом пути одна и та же сборка меряется от 15 до 63 Мбит/с. мерить будет {minutes(bench.benchingSeconds)} мин"
					>
						<Stepper
							value={`${bench.pairs}`}
							atMin={bench.pairs <= 1}
							atMax={bench.pairs >= 10}
							busy={benchSaving}
							ondown={() => saveBenchKnob({ pairs: bench!.pairs - 1 })}
							onup={() => saveBenchKnob({ pairs: bench!.pairs + 1 })}
						/>
					</Knob>
					<Knob label="длительность одного прогона">
						<Stepper
							value={minutes(bench.runSeconds)}
							unit="мин"
							atMin={bench.runSeconds <= 60}
							atMax={bench.runSeconds >= 600}
							busy={benchSaving}
							ondown={() => saveBenchKnob({ runSeconds: bench!.runSeconds - 60 })}
							onup={() => saveBenchKnob({ runSeconds: bench!.runSeconds + 60 })}
						/>
					</Knob>
					<Knob label="новая сборка держит не меньше">
						<Stepper
							value={`${bench.minThroughputPct}`}
							unit="% скорости"
							atMin={bench.minThroughputPct <= 10}
							atMax={bench.minThroughputPct >= 100}
							busy={benchSaving}
							ondown={() => saveBenchKnob({ minThroughputPct: bench!.minThroughputPct - 5 })}
							onup={() => saveBenchKnob({ minThroughputPct: bench!.minThroughputPct + 5 })}
						/>
					</Knob>
					<Knob label="и растит rtt не больше чем до">
						<Stepper
							value={`${bench.maxRttGrowthPct}`}
							unit="% прежнего"
							atMin={bench.maxRttGrowthPct <= 100}
							atMax={bench.maxRttGrowthPct >= 300}
							busy={benchSaving}
							ondown={() => saveBenchKnob({ maxRttGrowthPct: bench!.maxRttGrowthPct - 5 })}
							onup={() => saveBenchKnob({ maxRttGrowthPct: bench!.maxRttGrowthPct + 5 })}
						/>
					</Knob>
					{#if benchError}
						<p class="hint pad">{benchError}</p>
					{/if}
				{/if}
			</Panel>
			<!-- ======== 3. the fuse's own record: the autopilot standing down ======== -->
			{#if pressure.waves.length > 0}
				<Panel title="паузы предохранителя" sub="автопилот стоял и не чинил">
					<div class="waves">
						{#each pressure.waves as w (w.pausedAt + w.cause)}
							<div class="wave-row">
								<span class="wave-cause">{CAUSE_LABEL[w.cause] ?? w.cause}</span>
								<span class="m wave-when">
									{new Date(w.pausedAt * 1000).toLocaleString('ru-RU', {
										day: '2-digit',
										month: '2-digit',
										hour: '2-digit',
										minute: '2-digit',
									})}
								</span>
								<span class="m">{w.boxes} машин</span>
								<span class="wave-state" class:bad={!w.lifted} class:ok={w.lifted}>
									{w.lifted ? 'снята' : 'стоит'}
								</span>
								<span class="wave-why">{w.reason}</span>
							</div>
						{/each}
					</div>
				</Panel>
			{/if}
		{/if}
	</div>
</PageShell>

<style>
	.saved {
		font-size: 11px;
		color: var(--ok);
	}

	/* ---------- fuse pauses ---------- */

	.waves {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.wave-row {
		display: flex;
		align-items: baseline;
		gap: 12px;
		font-size: 11px;
		color: var(--text-2);
	}

	.wave-cause {
		min-width: 170px;
		color: var(--text);
		font-weight: 500;
	}

	.wave-when {
		color: var(--text-3);
	}

	.wave-state.bad {
		color: var(--bad);
	}

	.wave-state.ok {
		color: var(--ok);
	}

	.wave-why {
		flex: 1;
		color: var(--text-3);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	/* ---------- vantages ---------- */

	.vcards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
		gap: 8px;
	}

	.vcard {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 6px;
		padding: 10px 12px;
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
		background: var(--inset);
		color: var(--text-2);
		text-align: left;
		cursor: pointer;
		transition: border-color 0.15s, background 0.15s;
	}

	.vcard:hover {
		border-color: var(--border);
		background: var(--surface);
	}

	.vcard.active {
		border-color: var(--primary);
		background: var(--surface);
	}

	.vcard-top {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		font-size: 12px;
	}

	.vdot {
		width: 8px;
		height: 8px;
		border-radius: 2px;
		flex-shrink: 0;
	}

	.vname {
		color: var(--text);
		font-weight: 600;
	}

	.vtitle {
		color: var(--text-2);
		font-size: 11px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.vstale {
		margin-left: auto;
		font-size: 11px;
		color: var(--bad);
		font-weight: 500;
	}

	.vcard-machine {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
		font-size: 11px;
		color: var(--text-2);
	}

	.vbox {
		color: var(--text);
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		padding: 1px 5px;
		border-radius: 3px;
	}

	.vhost {
		color: var(--text-2);
	}

	.vprov {
		color: var(--text-3);
	}

	.vmachine-empty {
		color: var(--text-3);
	}

	.vcard-stats {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 11px;
		color: var(--text-3);
	}

	.vaction-btn {
		padding: 3px 9px;
		font-size: 11px;
		color: var(--text-2);
		background: var(--inset);
		border: 1px solid var(--border);
		border-radius: 5px;
		cursor: pointer;
	}

	.vaction-btn:hover {
		color: var(--text);
		background: var(--surface);
	}

	.vdetail {
		margin-top: 10px;
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--inset);
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.vdetail-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
	}

	.vdetail-meta {
		display: flex;
		align-items: baseline;
		gap: 8px;
	}

	.vdetail-slot {
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
	}

	.vdetail-title {
		font-size: 12px;
		color: var(--text-2);
	}

	.vkv-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
		gap: 8px;
		padding: 8px 0;
		border-top: 1px solid var(--border-subtle);
		border-bottom: 1px solid var(--border-subtle);
	}

	.vkv {
		display: flex;
		flex-direction: column;
		gap: 2px;
		font-size: 12px;
		color: var(--text);
	}

	.vlabel {
		font-size: 10px;
		color: var(--text-3);
	}

	.vtabs {
		display: flex;
		gap: 6px;
	}

	.vtab {
		padding: 4px 10px;
		font-size: 11px;
		color: var(--text-2);
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 5px;
		cursor: pointer;
	}

	.vtab.active {
		color: var(--text);
		border-color: var(--primary);
	}

	.vform {
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin-bottom: 8px;
	}

	.vform-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
		gap: 8px;
	}

	.vfield {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.vinput {
		padding: 6px 8px;
		font-size: 12px;
		color: var(--text);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 5px;
	}

	.vform-actions {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
	}

	.vbtn {
		padding: 5px 12px;
		font-size: 11px;
		border-radius: 5px;
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--text);
		cursor: pointer;
	}

	.vbtn.primary {
		background: var(--primary);
		border-color: var(--primary);
		color: #fff;
	}

	.vbtn.danger {
		color: var(--bad);
		border-color: var(--border);
	}

	.vbtn:disabled {
		opacity: 0.5;
		cursor: default;
	}

	.vdetail-actions {
		display: flex;
		gap: 8px;
		flex-wrap: wrap;
	}
	.vstale-text {
		color: var(--warn);
	}
	.vtoken {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px;
		margin-bottom: 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
	}
	.vtoken-head {
		display: flex;
		align-items: baseline;
		gap: 10px;
		flex-wrap: wrap;
	}
	.vtoken-note {
		font-size: 12px;
		color: var(--warn);
	}
	.vtoken-row {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}
	.vtoken-val {
		font-size: 12px;
		word-break: break-all;
		user-select: all;
	}
	.vtoken-cmd {
		margin: 0;
		padding: 8px;
		font-size: 12px;
		white-space: pre-wrap;
		word-break: break-all;
		border-radius: 6px;
		background: var(--inset);
	}
	.vorder-wrap {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.vchart {
		margin-top: 12px;
	}

	.pad {
		padding: 11px 16px;
	}
</style>
