<script lang="ts">
	import { onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import FleetPoolChart from '$components/entry/FleetPoolChart.svelte';
	import FleetCensorTimeline from '$components/entry/FleetCensorTimeline.svelte';
	import FleetEconomics from '$components/entry/FleetEconomics.svelte';
	import FleetPark from '$components/entry/FleetPark.svelte';
	import BoxCard from '$components/entry/BoxCard.svelte';
	import OrderBoxCard from '$components/entry/OrderBoxCard.svelte';
	import NewBoxCard from '$components/entry/NewBoxCard.svelte';
	import JobPanel from '$components/entry/JobPanel.svelte';
	import { formatRelativeTime } from '$lib/utils/format';
	import {
		fetchFleet,
		markRelayDead,
		retireBox,
		forgetHostKey,
		provisionBox,
		describeEntryError,
		deathLabel,
		formatCents,
		DEATH_REASONS,
		type DeathReason,
		type EntryFleet,
		type FleetBox,
		type FleetRelay,
		type OverviewWindow,
	} from '$lib/api/entry';

	// Fleet: the operational fleet, flow rates, rollouts, and unit economics,
	// all in one request — the provider comparison rides along in it.
	let fleet = $state<EntryFleet | null>(null);
	// The machine whose card is open; the card makes its own one request.
	let cardBox = $state<string | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<OverviewWindow>('7d');
	const WINDOWS: OverviewWindow[] = ['24h', '7d', '30d'];

	let showNewBoxModal = $state(false);

	let followed = $state<string | null>(null);
	let boxError = $state<string | null>(null);
	let killNote = $state<string | null>(null);

	// Modals for actions
	let dying = $state<FleetRelay | null>(null);
	let dyingReason = $state<DeathReason>('ip_banned');
	let retiring = $state<FleetBox | null>(null);
	let retireReason = $state<DeathReason>('rotated');
	let forgetting = $state<FleetBox | null>(null);
	let acting = $state(false);

	async function load() {
		try {
			fleet = await fetchFleet(window);
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

	function windowLabel(w: OverviewWindow): string {
		if (w === '24h') return '24 ч';
		if (w === '7d') return '7 дн';
		if (w === '30d') return '30 дн';
		return w;
	}

	// The page does not open a run's log by itself: a rollout the autopilot
	// started is a line in the list at the bottom, and its log is one click
	// away there. Only a run the operator started from this page is followed.
	onMount(load);

	function follow(jobId: string) {
		followed = jobId;
		boxError = null;
	}

	/** The list's «лог» button: opens a run's log under its line, or folds it. */
	function toggleLog(jobId: string) {
		followed = followed === jobId ? null : jobId;
	}

	async function onRetry(boxName: string) {
		try {
			const { jobId } = await provisionBox(boxName);
			follow(jobId);
			await load();
		} catch (e) {
			boxError = describeEntryError(e);
		}
	}

	async function onForget(box: FleetBox) {
		if (!confirm(`Забыть ключ хоста ${box.name}? Следующий разворот примет новый ключ.`)) return;
		try {
			await forgetHostKey(box.name);
			killNote = `Ключ хоста ${box.name} забыт`;
			await load();
		} catch (e) {
			boxError = describeEntryError(e);
		}
	}

	function askDead(r: FleetRelay) {
		dying = r;
		dyingReason = 'ip_banned';
	}

	async function confirmDead() {
		if (!dying || acting) return;
		acting = true;
		try {
			const { revokedGrants } = await markRelayDead(dying.name, dyingReason);
			killNote = `${dying.name} помечен мёртвым (${deathLabel(dyingReason)}), отозвано выдач: ${revokedGrants}`;
			dying = null;
			await load();
		} catch (e) {
			boxError = describeEntryError(e);
		} finally {
			acting = false;
		}
	}

	function askRetire(b: FleetBox, _relays: FleetRelay[]) {
		retiring = b;
		retireReason = b.openIncident?.includes('ip') ? 'ip_banned' : 'rotated';
	}

	async function confirmRetire() {
		if (!retiring || acting) return;
		acting = true;
		try {
			const res = await retireBox(retiring.name, retireReason);
			killNote = `${retiring.name} сдана, релеев снято: ${res.relays.length}, держателей с заменой: ${res.holders}`;
			if (res.jobId) follow(res.jobId);
			retiring = null;
			await load();
		} catch (e) {
			boxError = describeEntryError(e);
		} finally {
			acting = false;
		}
	}

	function formatTTR(sec: number): string {
		if (sec < 60) return `${Math.round(sec)} с`;
		if (sec < 3600) return `${Math.round(sec / 60)} мин`;
		return `${(sec / 3600).toFixed(1)} ч`;
	}
</script>

<PageShell>
	<div class="fleet-page">
		<!-- ---------- head ---------- -->
		<div class="head">
			<div>
				<h1>машины</h1>
			</div>
			<div class="right">
				<div class="ranges">
					{#each WINDOWS as w (w)}
						<button class="chip" class:on={window === w} onclick={() => pick(w)}>
							{windowLabel(w)}
						</button>
					{/each}
				</div>
				<button class="primary" onclick={() => (showNewBoxModal = !showNewBoxModal)}> + завести машину </button>
			</div>
		</div>

		{#if loadError}
			<p class="err" role="alert">{loadError}</p>
		{:else if !fleet}
			<p class="hint">загружаю парк…</p>
		{:else}
			{#if killNote}<p class="note-success">{killNote}</p>{/if}
			{#if boxError}<p class="err" role="alert">{boxError}</p>{/if}

			{#if showNewBoxModal}
				<div class="modal-card">
					<div class="modal-head">
						<h3>Завести машину</h3>
						<button class="chip" onclick={() => (showNewBoxModal = false)}>закрыть</button>
					</div>
					<div class="modal-body">
						<OrderBoxCard onstarted={(id) => (follow(id), (showNewBoxModal = false), load())} />
						<NewBoxCard
							taken={fleet.boxes.map((b) => b.name)}
							onstarted={(id) => (follow(id), (showNewBoxModal = false), load())}
						/>
					</div>
				</div>
			{/if}

			<!-- ======== 1. POOL CAPACITY (COMPACT TOP BANNER) ======== -->
			<section class="card pool-card-compact">
				<div class="pool-metrics">
					<div class="p-cell">
						<span class="p-label">релеи в пуле</span>
						<div class="p-val m v-ok">
							{fleet.pool.inDistribution.toLocaleString('ru-RU')}
						</div>
					</div>

					<div class="p-cell">
						<span class="p-label">машины</span>
						<div class="p-val m">
							{fleet.pool.boxesCount.toLocaleString('ru-RU')}
						</div>
					</div>
				</div>

				<div class="pool-graph">
					<FleetPoolChart series={fleet.pool.series} bucketSeconds={fleet.bucketSeconds} height="75px" />
				</div>
			</section>

			<!-- ======== 2. HERO: Rollouts & Blocks ======== -->
			<section class="card hero-censor-card">
				<div class="card-head">
					<h2>развороты и блокировки</h2>
				</div>

				<!-- 3 Top Metric Figures -->
				<div class="hero-strip">
					<div class="h-cell">
						<div class="h-label">развороты в сутки</div>
						<div class="h-val m v-ok">
							{fleet.strip.deathsVsReplacements.replacementsDay.toLocaleString('ru-RU')}
						</div>
					</div>

					<div class="h-cell">
						<div class="h-label">блокировки в сутки</div>
						<div class="h-val m v-bad">
							{fleet.strip.deathsVsReplacements.deathsDay.toLocaleString('ru-RU')}
						</div>
					</div>

					<div class="h-cell">
						<div class="h-label">время замены (TTR)</div>
						<div class="h-val m">
							{#if fleet.strip.timeToReplacement.medianSec !== null}
								{formatTTR(fleet.strip.timeToReplacement.medianSec)}
							{:else}
								<span class="h-unit muted">—</span>
							{/if}
						</div>
					</div>
				</div>

				<!-- History Timeline Chart -->
				<div class="timeline-box">
					<FleetCensorTimeline
						series={fleet.strip.deathsVsReplacements.series}
						bucketSeconds={fleet.bucketSeconds}
						height="180px"
					/>
				</div>
			</section>

			<!-- ======== 3. PRODUCT & BIZOPS ECONOMICS ======== -->
			<FleetEconomics compare={fleet.providers} />

			<!-- ======== 4. FLEET PARK (GEO GROUPS & EXPANDABLE LIST) ======== -->
			<FleetPark
				{fleet}
				onRetireBox={askRetire}
				onForgetBox={onForget}
				onMarkDeadRelay={askDead}
				onOpenBox={(b) => (cardBox = b.name)}
			/>

			<!-- ======== 5. ROLLOUTS IN FLIGHT (BOTTOM PANEL) ======== -->
			{#if fleet.jobs.length > 0}
				<section class="card rollouts-compact-card">
					<div class="card-head">
						<div class="head-title">
							<h2>развороты</h2>
							<span class="sub">
								{#if fleet.jobs.some((j) => j.status === 'running')}
									<span class="running-indicator"></span> идёт выполнение
								{:else}
									{fleet.jobs.length} в истории
								{/if}
							</span>
						</div>
					</div>

					<div class="jobs-strip">
						{#each fleet.jobs as j (j.id)}
							<div class="jobline" class:running={j.status === 'running'}>
								<span class="jname">{j.box}</span>
								<span class="jstatus js-{j.status}">
									{j.status === 'running' ? 'идёт' : j.status === 'done' ? 'готово' : 'упало'}
								</span>
								<span class="step">
									{j.step}{j.error ? `: ${j.error}` : ''}
								</span>
								{#if j.canRetry}
									<button class="retry" onclick={() => onRetry(j.box)}>повторить</button>
								{/if}
								<button class="btn view-log" onclick={() => toggleLog(j.id)}>
									{followed === j.id ? 'скрыть' : 'лог'}
								</button>
								<span class="ago">{formatRelativeTime(j.createdAt)}</span>
							</div>
							{#if j.status === 'running'}
								<div class="prog"></div>
							{/if}
							{#if followed === j.id}
								<JobPanel jobId={j.id} onfinished={load} />
							{/if}
						{/each}
					</div>
				</section>
			{/if}

			<!-- A run started from this page that the list does not hold yet. -->
			{#if followed && !fleet.jobs.some((j) => j.id === followed)}
				<JobPanel jobId={followed} onfinished={load} />
			{/if}
		{/if}
	</div>
</PageShell>

{#if cardBox}
	<BoxCard name={cardBox} onclose={() => (cardBox = null)} />
{/if}

<!-- Dialog for relay death -->
{#if dying}
	<div class="modal-backdrop">
		<div class="modal-box">
			<h3>Пометить {dying.name} мёртвым</h3>
			<p class="modal-hint">Релей будет немедленно исключён из пула раздачи клиентам.</p>
			<label class="modal-label">
				Причина:
				<select bind:value={dyingReason} class="modal-select">
					{#each DEATH_REASONS as reason (reason.id)}
						<option value={reason.id}>{reason.label}</option>
					{/each}
				</select>
			</label>
			<div class="modal-acts">
				<button class="btn" disabled={acting} onclick={() => (dying = null)}>Отмена</button>
				<button class="btn danger" disabled={acting} onclick={confirmDead}>
					{acting ? 'Помечаю…' : 'Пометить мёртвым'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Dialog for retiring box -->
{#if retiring}
	<div class="modal-backdrop">
		<div class="modal-box">
			<h3>Сдать машину {retiring.name}</h3>
			<p class="modal-hint">Все релеи на машине будут сняты, а держателям будет выдана замена.</p>
			<label class="modal-label">
				Причина сдачи:
				<select bind:value={retireReason} class="modal-select">
					{#each DEATH_REASONS as reason (reason.id)}
						<option value={reason.id}>{reason.label}</option>
					{/each}
				</select>
			</label>
			<div class="modal-acts">
				<button class="btn" disabled={acting} onclick={() => (retiring = null)}>Отмена</button>
				<button class="btn danger" disabled={acting} onclick={confirmRetire}>
					{acting ? 'Сдаю…' : 'Сдать машину'}
				</button>
			</div>
		</div>
	</div>
{/if}

<style>
	.fleet-page {
		--bg: oklch(0.14 0 0);
		--bg-raised: oklch(0.17 0 0);
		--surface: oklch(0.23 0 0);
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
		max-width: 1400px;
	}

	.head {
		display: flex;
		align-items: flex-end;
		gap: 16px;
		margin-bottom: 18px;
	}

	.head h1 {
		margin: 0;
		font-size: 20px;
		font-weight: 600;
		letter-spacing: -0.01em;
		color: var(--text);
	}

	.head-sub {
		margin: 3px 0 0;
		font-size: 12px;
		color: var(--text-2);
	}

	.head .right {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: 10px;
	}

	.ranges {
		display: flex;
		gap: 4px;
	}

	.chip {
		padding: 4px 11px;
		border-radius: 6px;
		font-size: 11px;
		font-family: var(--sans);
		color: var(--text-2);
		border: 1px solid var(--border);
		background: none;
		white-space: nowrap;
		cursor: pointer;
		transition:
			background 0.12s,
			color 0.12s,
			border-color 0.12s;
	}

	.chip:hover {
		background: var(--surface);
	}

	.chip.on {
		color: var(--text);
		background: var(--primary-subtle);
		border-color: oklch(0.75 0.14 350 / 0.35);
	}

	.primary {
		padding: 7px 14px;
		border-radius: 7px;
		font-size: 12px;
		font-weight: 500;
		font-family: var(--sans);
		color: var(--bg);
		background: var(--primary);
		white-space: nowrap;
		border: none;
		cursor: pointer;
		transition: background 0.12s;
	}

	.primary:hover {
		background: oklch(0.8 0.14 350);
	}

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

	.card-head .sub {
		font-size: 11px;
		color: var(--text-3);
	}

	.card-head .right {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.note {
		margin: 0;
		padding: 11px 16px 14px;
		font-size: 11px;
		color: var(--text-3);
	}

	/* ---------- compact pool banner ---------- */
	.pool-card-compact {
		display: grid;
		grid-template-columns: auto 1fr;
		align-items: center;
		gap: 32px;
		padding: 12px 20px;
		margin-bottom: 16px;
	}

	.pool-metrics {
		display: flex;
		gap: 36px;
	}

	.p-cell {
		display: flex;
		flex-direction: column;
		gap: 3px;
	}

	.p-label {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-2);
		font-weight: 500;
	}

	.p-val {
		display: flex;
		align-items: baseline;
		gap: 6px;
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-size: 24px;
		font-weight: 600;
		letter-spacing: -0.02em;
		color: var(--text);
	}

	.p-trend {
		font-size: 13px;
		font-weight: 500;
	}
	.p-trend.good {
		color: var(--ok);
	}
	.p-trend.bad {
		color: var(--bad);
	}

	.pool-graph {
		width: 100%;
		min-width: 0;
	}

	/* ---------- hero censor card & strip ---------- */
	.hero-censor-card {
		margin-bottom: 16px;
	}

	.hero-strip {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 1px;
		background: var(--border-subtle);
		border-bottom: 1px solid var(--border-subtle);
	}

	.h-cell {
		background: var(--card-bg);
		padding: 12px 18px;
	}

	.h-label {
		display: block;
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-2);
		font-weight: 500;
	}

	.h-val {
		display: flex;
		align-items: baseline;
		gap: 6px;
		margin-top: 3px;
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-size: 23px;
		font-weight: 600;
		letter-spacing: -0.02em;
		color: var(--text);
	}

	.h-unit {
		font-size: 13px;
		font-weight: 400;
		color: var(--text-3);
	}

	.timeline-box {
		padding: 10px 18px 12px;
	}

	.pool-card {
		margin-bottom: 24px;
	}

	.price-tag {
		background: oklch(0.72 0.15 155 / 0.12);
		color: var(--ok);
		border: 1px solid oklch(0.72 0.15 155 / 0.25);
		font-family: var(--mono);
		font-weight: 500;
	}

	.v-warn {
		color: var(--warn);
	}
	.v-bad {
		color: var(--bad);
	}
	.v-ok {
		color: var(--ok);
	}

	.slash {
		color: var(--text-3);
		font-weight: 400;
	}

	/* ---------- rollouts (jobs) - compact bottom strip ---------- */
	.rollouts-compact-card {
		margin-bottom: 24px;
	}

	.rollouts-compact-card .card-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.rollouts-compact-card .head-title {
		display: flex;
		align-items: baseline;
		gap: 10px;
	}

	.running-indicator {
		display: inline-block;
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--ok);
		box-shadow: 0 0 6px var(--ok);
	}

	.jobs-strip {
		max-height: 380px;
		overflow-y: auto;
	}

	.jobline {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 8px 16px;
		border-bottom: 1px solid var(--border-subtle);
	}

	.jobline:last-child {
		border-bottom: none;
	}

	.jobline.running {
		background: var(--primary-subtle);
	}

	.jobline .jname {
		font-family: var(--mono);
		font-size: 12px;
		font-weight: 600;
		width: 90px;
		flex-shrink: 0;
		color: var(--text);
	}

	.jstatus {
		padding: 2px 8px;
		border-radius: 4px;
		font-size: 11px;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.js-running {
		color: var(--primary);
		background: var(--primary-subtle);
	}
	.js-done {
		color: var(--ok);
		background: oklch(0.72 0.15 155 / 0.12);
	}
	.js-failed {
		color: var(--bad);
		background: oklch(0.65 0.2 25 / 0.12);
	}

	.jobline .step {
		flex: 1;
		min-width: 0;
		font-size: 12px;
		color: var(--text-2);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.jobline .ago {
		font-family: var(--mono);
		font-size: 10px;
		color: var(--text-3);
		white-space: nowrap;
	}

	.jobline .retry {
		font-size: 11px;
		color: var(--text-3);
		border: 1px solid var(--border);
		border-radius: 5px;
		padding: 3px 9px;
		background: none;
		cursor: pointer;
	}

	.jobline .retry:hover {
		color: var(--text);
		background: var(--surface);
	}

	.view-log {
		font-size: 10px;
		padding: 2px 7px;
	}

	.prog {
		height: 2px;
		background: var(--primary);
		animation: slide 1.6s ease-in-out infinite;
	}

	@keyframes slide {
		0% {
			margin-left: 0;
			width: 20%;
		}
		50% {
			margin-left: 40%;
			width: 45%;
		}
		100% {
			margin-left: 100%;
			width: 10%;
		}
	}

	/* ---------- fleet machines ---------- */
	.filters {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.machine {
		border-bottom: 1px solid var(--border-subtle);
	}

	.machine:last-child {
		border-bottom: none;
	}

	.m-head {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 12px 16px 10px;
	}

	.m-head .mname {
		font-family: var(--mono);
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
	}

	.badge {
		padding: 2px 7px;
		border-radius: 4px;
		font-size: 10px;
		white-space: nowrap;
		background: var(--surface);
		color: var(--text-2);
	}

	.badge.proto-reality {
		color: var(--c-reality);
		background: oklch(0.75 0.14 350 / 0.12);
	}

	.badge.proto-hyst {
		color: var(--c-hyst);
		background: oklch(0.72 0.12 200 / 0.12);
	}

	.badge.proto-xhttp {
		color: var(--c-xhttp);
		background: oklch(0.78 0.12 85 / 0.12);
	}

	.verdict {
		padding: 2px 8px;
		border-radius: 4px;
		font-size: 11px;
		white-space: nowrap;
	}

	.ver-ok {
		color: var(--ok);
		background: oklch(0.72 0.15 155 / 0.12);
	}
	.ver-warn {
		color: var(--warn);
		background: oklch(0.78 0.12 85 / 0.12);
	}
	.ver-bad {
		color: var(--bad);
		background: oklch(0.65 0.2 25 / 0.12);
	}
	.ver-mute {
		color: var(--text-3);
		background: oklch(0.52 0 0 / 0.12);
	}

	.m-acts {
		margin-left: auto;
		display: flex;
		gap: 4px;
	}

	.btn {
		padding: 4px 11px;
		font-size: 11px;
		color: var(--text-2);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 6px;
		white-space: nowrap;
		cursor: pointer;
		text-decoration: none;
		transition:
			background 0.1s,
			color 0.1s;
	}

	.btn:hover {
		color: var(--text);
		background: var(--bg-raised);
	}

	.btn.fix {
		color: var(--primary);
		border-color: oklch(0.75 0.14 350 / 0.4);
	}

	.btn.fix:hover {
		color: var(--primary);
		background: var(--primary-subtle);
	}

	.btn.danger:hover {
		color: var(--bad);
		border-color: oklch(0.65 0.2 25 / 0.5);
	}

	/* Relay rows */
	.r-grid {
		padding: 0 16px 12px;
	}

	.r-head,
	.r-row {
		display: grid;
		grid-template-columns: 150px 150px minmax(120px, 1fr) 62px 96px 92px;
		align-items: center;
		gap: 14px;
	}

	.r-head {
		padding-bottom: 5px;
		font-size: 10px;
		color: var(--text-3);
	}

	.r-head span:nth-child(4),
	.r-head span:nth-child(5),
	.r-row .grants,
	.r-row .ttl {
		text-align: right;
	}

	.r-row {
		padding: 5px 0;
	}

	.r-row .rname {
		display: flex;
		align-items: center;
		gap: 7px;
		font-family: var(--mono);
		font-size: 11px;
		color: var(--text);
	}

	.tag-kind {
		font-family: var(--sans);
		font-size: 10px;
		color: var(--text-3);
	}

	.r-row .cover {
		font-family: var(--mono);
		font-size: 11px;
		color: var(--text-2);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.r-row .grants,
	.r-row .ttl {
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
		font-size: 11px;
		color: var(--text-2);
		white-space: nowrap;
	}

	.r-row .act {
		text-align: right;
	}

	.r-row .kill {
		font-size: 11px;
		color: var(--text-3);
		padding: 2px 7px;
		border-radius: 5px;
		background: none;
		border: none;
		cursor: pointer;
	}

	.r-row .kill:hover {
		color: var(--bad);
		background: oklch(0.65 0.2 25 / 0.1);
	}

	.dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		flex-shrink: 0;
		display: inline-block;
	}

	.dot.ok {
		background: var(--ok);
	}
	.dot.warn {
		background: var(--warn);
	}
	.dot.bad {
		background: var(--bad);
	}
	.dot.mute {
		background: var(--unknown);
	}

	.fold {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		padding: 12px 16px;
		text-align: left;
		font-size: 12px;
		color: var(--text-3);
		border-top: 1px solid var(--border-subtle);
		background: none;
		border-left: none;
		border-right: none;
		border-bottom: none;
		cursor: pointer;
	}

	.fold:hover {
		color: var(--text-2);
		background: var(--inset);
	}

	.fold .caret {
		font-family: var(--mono);
		color: var(--text-3);
	}

	.fold .dots {
		margin-left: auto;
		display: flex;
		gap: 2px;
	}

	.fold .dots i {
		width: 5px;
		height: 5px;
		border-radius: 50%;
		background: var(--ok);
		display: inline-block;
	}

	.provider-group {
		border-bottom: 1px solid var(--border-subtle);
	}

	.provider-title {
		padding: 10px 16px;
		font-size: 11px;
		color: var(--text-2);
		background: var(--inset);
		border-bottom: 1px solid var(--border-subtle);
	}

	.empty {
		padding: 24px 16px;
		font-size: 12px;
		color: var(--text-3);
		text-align: center;
	}

	.err {
		margin: 0 0 16px;
		font-size: 12px;
		color: var(--bad);
	}

	.hint {
		margin: 0 0 16px;
		font-size: 12px;
		color: var(--text-3);
	}

	.note-success {
		margin: 0 0 16px;
		padding: 8px 12px;
		border-radius: 6px;
		background: oklch(0.72 0.15 155 / 0.12);
		color: var(--ok);
		font-size: 12px;
	}

	/* Modal / dialog overlays */
	.modal-backdrop {
		position: fixed;
		top: 0;
		left: 0;
		right: 0;
		bottom: 0;
		background: rgba(0, 0, 0, 0.7);
		display: flex;
		align-items: center;
		justify-content: center;
		z-index: 1000;
	}

	.modal-box {
		background: var(--bg-raised);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 20px;
		max-width: 440px;
		width: 100%;
		box-shadow: 0 8px 24px rgba(0, 0, 0, 0.5);
	}

	.modal-box h3 {
		margin: 0 0 8px;
		font-size: 14px;
		font-weight: 600;
		color: var(--text);
	}

	.modal-hint {
		margin: 0 0 16px;
		font-size: 12px;
		color: var(--text-2);
	}

	.modal-label {
		display: block;
		font-size: 12px;
		color: var(--text-2);
		margin-bottom: 16px;
	}

	.modal-select {
		display: block;
		width: 100%;
		margin-top: 6px;
		padding: 6px 10px;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text);
		font-size: 12px;
	}

	.modal-acts {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
	}

	.modal-card {
		background: var(--card-bg);
		border: 1px solid var(--border);
		border-radius: 8px;
		margin-bottom: 16px;
		padding: 16px;
	}

	.modal-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 14px;
	}

	.modal-head h3 {
		margin: 0;
		font-size: 14px;
		font-weight: 600;
	}

	@media (max-width: 1100px) {
		.strip,
		.pool-hero {
			grid-template-columns: 1fr;
		}

		.r-head,
		.r-row {
			grid-template-columns: 120px 1fr 70px;
		}

		.r-head span:nth-child(2),
		.r-head span:nth-child(4),
		.r-row .cover:first-of-type,
		.r-row .grants {
			display: none;
		}
	}
</style>
