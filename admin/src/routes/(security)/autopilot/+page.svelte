<script lang="ts">
	import { onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import Panel from '$components/entry/ui/Panel.svelte';
	import Knob from '$components/entry/ui/Knob.svelte';
	import Stepper from '$components/entry/ui/Stepper.svelte';
	import AutopilotCharts from '$components/entry/AutopilotCharts.svelte';
	import SpendCapsCard from '$components/entry/SpendCapsCard.svelte';
	import {
		fetchDetectionPolicy,
		saveDetectionPolicy,
		fetchPlaybookModes,
		savePlaybookMode,
		fetchFleetPools,
		fetchFleetChart,
		fetchSpend,
		saveSpendCaps,
		fetchSuspects,
		describeEntryError,
		CAUSE_LABEL,
		PLAYBOOK_LABEL,
		type DetectionPolicy,
		type SecondChance,
		type Suspect,
		type SuspectKind,
		type PlaybookMode,
		type PlaybookModeRow,
		type FleetPool,
		type FleetReading,
		type FleetChart,
		type SpendReading,
	} from '$lib/api/entry';

	// The autopilot screen (roadmap 42 П9.10, П9.11, П9.16): every knob a
	// person turns to tell the hub what to do by itself, in one place — which
	// repairs it may run unasked, when it evicts, and how long an evicted thing
	// gets before it is written off. How many machines a pool holds is not one
	// of them: the hub derives it from the pool's load, and the screen shows
	// what it derived and from what.
	// What the hub sees (the prober) and what it did (incidents) are their own
	// screens; this one only sets. Every save is one knob, behind the step-up
	// where it lets the hub spend money later.

	const MODES: readonly { key: PlaybookMode; label: string }[] = [
		{ key: 'auto', label: 'авто' },
		{ key: 'ask', label: 'спросить' },
		{ key: 'off', label: 'выкл' },
	];

	const PROTO_LABEL: Record<string, string> = {
		'vless-reality-vision': 'reality',
		'vless-xhttp': 'xhttp',
		hysteria2: 'голос',
	};

	// The second step of a consumable's life (П9.11): out of the pool on the
	// eviction threshold, back after clean reprobes on separate days, written
	// off when they fail or the deadline passes. The hint is why the default
	// is what it is, so a person turning the knob sees what it trades.
	const SECOND_CHANCE: readonly { kind: SuspectKind; label: string; hint: string }[] = [
		{
			kind: 'domain',
			label: 'домен',
			hint: 'уже оплачен до продления — держать под вопросом почти бесплатно; срок решает, продлевать ли',
		},
		{
			kind: 'box',
			label: 'машина',
			hint: 'под вопросом — почасовая аренда без людей; два провала с разницей в сутки = бан пережил обновление списков',
		},
		{
			kind: 'cover',
			label: 'прикрытие',
			hint: 'чужой сервер, спрашивать бесплатно; проверенное через ТСПУ стоит ждать',
		},
	];

	const KIND_LABEL: Record<SuspectKind, string> = { domain: 'домен', box: 'машина', cover: 'прикрытие' };
	const ENDED_LABEL: Record<string, string> = { returned: 'вернулся', dead: 'списан', withdrawn: 'снят' };

	let policy = $state<DetectionPolicy | null>(null);
	let suspects = $state<Suspect[]>([]);
	let modes = $state<PlaybookModeRow[] | null>(null);
	let fleet = $state<FleetReading | null>(null);
	// One snapshot holds the aggregate and protocol charts on matching axes.
	let charts = $state<FleetChart | null>(null);
	let chartError = $state<string | null>(null);
	let spend = $state<SpendReading[]>([]);
	let moneyMode = $state(true);
	const pools = $derived(fleet?.pools ?? null);

	let loadError = $state<string | null>(null);
	let saveError = $state<string | null>(null);
	let saving = $state<string | null>(null);
	let saved = $state<string | null>(null);

	function minutes(sec: number): string {
		return sec % 60 === 0 ? `${sec / 60}` : (sec / 60).toFixed(1);
	}

	function poolName(p: FleetPool): string {
		const proto = PROTO_LABEL[p.proto] ?? p.proto;
		return p.voiceRegion ? `${proto} · ${p.voiceRegion}` : proto;
	}

	/** What the hub derived for a pool, in one line under its name. */
	function poolHint(p: FleetPool): string {
		const head = `живых ${p.live}`;
		const going = p.leaving > 0 ? ` · уходит ${p.leaving}` : '';
		const ahead = p.buffer > 0 ? ` · запас на потери +${p.buffer}` : '';
		const short = p.need > 0 ? ` · не хватает ${p.need}` : '';
		return `${head}${going}${ahead}${short} · ${p.targetWhy} · ${p.why}`;
	}

	// One wrapper for every knob on the screen: the key says which one is in
	// flight, a refusal is shown as the hub worded it, and the screen re-reads
	// what the hub holds rather than keeping what was asked for.
	async function save(key: string, act: () => Promise<void>) {
		if (saving !== null) return;
		saving = key;
		saveError = null;
		try {
			await act();
			saved = key;
			setTimeout(() => {
				if (saved === key) saved = null;
			}, 2000);
		} catch (e) {
			saveError = describeEntryError(e);
			await load();
		} finally {
			saving = null;
		}
	}

	const saveKnob = (patch: Parameters<typeof saveDetectionPolicy>[0]) =>
		save('policy', async () => {
			policy = await saveDetectionPolicy(patch);
		});

	const setMode = (row: PlaybookModeRow, mode: PlaybookMode) =>
		save('modes', async () => {
			modes = (await savePlaybookMode(row.cause, mode)).modes;
		});

	const hours = (sec: number) => `${Math.round(sec / 3600)}`;

	/** The domain pool in one line: what is free, what is in doubt, why it buys or does not. */
	function domainHint(): string {
		const d = fleet?.domainPool;
		if (!d) return '';
		const doubt = d.unknown > 0 ? ` · без ответа регистратора ${d.unknown} — повторю с тем же ключом` : '';
		return `свободных ${d.free}${doubt} · ${d.why}`;
	}
	const days = (sec: number) => (sec % 86400 === 0 ? `${sec / 86400}` : (sec / 86400).toFixed(1));
	const date = (iso: string) =>
		new Date(iso).toLocaleString('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });

	/** One suspicion in one line: how far its second chance got, or how it ended. */
	function suspectHint(x: Suspect): string {
		if (x.status === 'suspect') {
			const asked = x.lastProbeAt ? `проба ${date(x.lastProbeAt)}` : 'ещё не пробовали';
			return `с ${date(x.since)} (${x.reason}) · ${asked} · чистых подряд ${x.cleanStreak} · провалов ${x.failedProbes}`;
		}
		return `${ENDED_LABEL[x.status] ?? x.status} ${x.endedAt ? date(x.endedAt) : ''}: ${x.outcome ?? ''}`;
	}

	/** One knob of one kind's second chance; the bounds and the rules are the hub's (073). */
	const saveChance = (kind: SuspectKind, patch: Partial<SecondChance>) => saveKnob({ secondChance: { [kind]: patch } });

	async function load() {
		try {
			const [p, m, f, sx, budget] = await Promise.all([
				fetchDetectionPolicy(),
				fetchPlaybookModes(),
				fetchFleetPools(),
				fetchSuspects(),
				fetchSpend(),
			]);
			policy = p;
			spend = budget.spend;
			suspects = sx.suspects;
			modes = m.modes;
			fleet = f;
			loadError = null;
			void loadCharts();
		} catch (e) {
			loadError = describeEntryError(e);
		}
	}

	// The charts come after the screen: a slow dataset must not hold up the
	// knobs, and a chart that cannot be read costs that chart, not the page.
	async function loadCharts() {
		try {
			charts = await fetchFleetChart();
			chartError = null;
		} catch (e) {
			chartError = describeEntryError(e);
		}
	}

	const saveBudget = (role: SpendReading['role'], daily: number, monthly: number) =>
		save(`budget-${role}`, async () => {
			spend = (await saveSpendCaps(role, daily, monthly)).spend;
			await loadCharts();
		});

	onMount(() => {
		void load();
	});
</script>

<PageShell>
	<div class="entry-scope">
		<div class="head">
			<h1>автопилот</h1>
		</div>

		{#if saveError}
			<p class="err-banner" role="alert">{saveError}</p>
		{/if}

		{#if loadError}
			<p class="err-banner" role="alert">{loadError}</p>
		{:else if !policy || !modes || !pools}
			<p class="hint">загружаю</p>
		{:else}
			<Panel
				title="бюджет автоматики"
				sub="в пределах дня и месяца покупки выполняются автоматически · USD · периоды UTC"
			>
				<div class="budgets">
					{#each spend as reading (reading.role)}
						<SpendCapsCard
							{reading}
							busy={saving !== null}
							saved={saved === `budget-${reading.role}`}
							onsave={(daily, monthly) => saveBudget(reading.role, daily, monthly)}
						/>
					{/each}
				</div>
			</Panel>
			<Panel title="весь парк" sub="7 дней истории · 24 часа прогноза пунктиром">
				{#snippet controls()}
					<div class="seg" role="group" aria-label="Единицы графиков">
						<button class:on={moneyMode} onclick={() => (moneyMode = true)}>$ / день</button>
						<button class:on={!moneyMode} onclick={() => (moneyMode = false)}>штуки</button>
					</div>
				{/snippet}
				{#if chartError}
					<p role="alert">{chartError}</p>
					<button onclick={loadCharts}>Повторить загрузку графиков</button>
				{:else if !charts}<p class="hint">загружаю графики</p>{/if}
				<AutopilotCharts set={charts?.total} usesDomains {moneyMode} {spend} />
			</Panel>
			{#each fleet!.warnings as w (w)}<p class="warn-line" role="alert">{w}</p>{/each}
			{#each pools as p (p.pool)}
				<Panel title={poolName(p)} sub={poolHint(p)}>
					<p class="derived">Цель {p.target + p.buffer} машин{p.proto === 'vless-xhttp' ? ` · ${domainHint()}` : ''}</p>
					{@const chart = charts?.pools.find((c) => c.pool === p.pool)}
					<AutopilotCharts set={chart} usesDomains={p.proto === 'vless-xhttp'} {moneyMode} />
				</Panel>
			{/each}

			{#each charts?.pools.filter((c) => !pools.some((p) => p.pool === c.pool)) ?? [] as chart (chart.pool)}
				<Panel title="hysteria2 · UDP"><AutopilotCharts set={chart} {moneyMode} /></Panel>
			{/each}

			<!-- ======== 2. which repairs the hub runs unasked ======== -->
			<Panel
				title="режимы ремонта"
				sub="авто — ремонт без подтверждения; спросить — запрос оператору в инцидентах; выкл — ремонт отключён"
				flush
			>
				{#snippet controls()}
					{#if saved === 'modes'}<span class="saved">сохранено</span>{/if}
				{/snippet}
				{#each modes as row (row.cause)}
					<Knob
						label="{CAUSE_LABEL[row.cause] ?? row.cause} → {PLAYBOOK_LABEL[row.playbook] ?? row.playbook}{row.spends
							? ' · тратит деньги'
							: ''}"
						hint="{row.why}{row.mode !== row.defaultMode
							? ` · по умолчанию «${MODES.find((m) => m.key === row.defaultMode)?.label}»`
							: ''}"
					>
						<div class="seg" role="group" aria-label="режим">
							{#each MODES as m (m.key)}
								<button
									type="button"
									class:on={row.mode === m.key}
									class:spend={m.key === 'auto' && row.spends}
									disabled={saving !== null || row.mode === m.key}
									onclick={() => setMode(row, m.key)}>{m.label}</button
								>
							{/each}
						</div>
					</Knob>
				{/each}
			</Panel>

			<!-- ======== 3. when the hub evicts and gives up ======== -->
			<Panel
				title="правила ремонта и возврата"
				sub="базовые значения уже заданы — менять их для запуска не нужно; изменения применяются сразу"
				flush
			>
				{#snippet controls()}
					{#if saved === 'policy'}<span class="saved">сохранено</span>{/if}
				{/snippet}
				<fieldset class="policy-group">
					<legend>ремонт</legend>
					<Knob label="попыток ремонта до передачи оператору">
						<Stepper
							value={`${policy.failStrikes}`}
							atMin={policy.failStrikes <= 1}
							atMax={policy.failStrikes >= 10}
							busy={saving !== null}
							ondown={() => saveKnob({ failStrikes: policy!.failStrikes - 1 })}
							onup={() => saveKnob({ failStrikes: policy!.failStrikes + 1 })}
						/>
					</Knob>
					<Knob label="пауза после неудачного ремонта">
						<Stepper
							value={minutes(policy.repairCooldownSeconds)}
							unit="мин"
							atMin={policy.repairCooldownSeconds <= 60}
							atMax={policy.repairCooldownSeconds >= 86400}
							busy={saving !== null}
							ondown={() => saveKnob({ repairCooldownSeconds: policy!.repairCooldownSeconds - 60 })}
							onup={() => saveKnob({ repairCooldownSeconds: policy!.repairCooldownSeconds + 60 })}
						/>
					</Knob>
				</fieldset>
				<fieldset class="policy-group">
					<legend>массовый сбой</legend>
					<p class="group-hint">
						Несколько машин с одной причиной за это окно останавливают автоматический ремонт этой причины до решения
						оператора.
					</p>
					<Knob label="машин с одной причиной за окно = волна">
						<Stepper
							value={`${policy.waveLimit}`}
							atMin={policy.waveLimit <= 1}
							atMax={policy.waveLimit >= 20}
							busy={saving !== null}
							ondown={() => saveKnob({ waveLimit: policy!.waveLimit - 1 })}
							onup={() => saveKnob({ waveLimit: policy!.waveLimit + 1 })}
						/>
					</Knob>
					<Knob label="окно волны">
						<Stepper
							value={minutes(policy.waveWindowSeconds)}
							unit="мин"
							atMin={policy.waveWindowSeconds <= 300}
							atMax={policy.waveWindowSeconds >= 86400}
							busy={saving !== null}
							ondown={() => saveKnob({ waveWindowSeconds: policy!.waveWindowSeconds - 300 })}
							onup={() => saveKnob({ waveWindowSeconds: policy!.waveWindowSeconds + 300 })}
						/>
					</Knob>
				</fieldset>
				<fieldset class="policy-group">
					<legend>подтверждение сбоя</legend>
					<Knob label="срок годности пробы">
						<Stepper
							value={minutes(policy.verdictWindowSeconds)}
							unit="мин"
							atMin={policy.verdictWindowSeconds <= 60}
							atMax={policy.verdictWindowSeconds >= 3600}
							busy={saving !== null}
							ondown={() => saveKnob({ verdictWindowSeconds: policy!.verdictWindowSeconds - 60 })}
							onup={() => saveKnob({ verdictWindowSeconds: policy!.verdictWindowSeconds + 60 })}
						/>
					</Knob>
					<Knob
						label="подтверждение из двух точек"
						hint={policy.vantages.length < 2
							? `точек наблюдения за сутки: ${policy.vantages.length} — включить нечем`
							: undefined}
					>
						<button
							type="button"
							class="toggle-btn"
							class:on={policy.requireTwoVantages}
							disabled={saving !== null || policy.vantages.length < 2}
							onclick={() => saveKnob({ requireTwoVantages: !policy!.requireTwoVantages })}
							aria-label="Подтверждение из двух точек"
						>
							<span class="toggle-knob"></span>
						</button>
					</Knob>
				</fieldset>

				<!-- the second chance: what the threshold evicts is suspect, not dead -->
				<p class="group">
					второй шанс: неисправный ресурс сразу исключается из выдачи. Чистые проверки возвращают его в работу;
					повторные сбои или истечение срока приводят к списанию.
				</p>
				{#each SECOND_CHANCE as k (k.kind)}
					{@const c = policy.secondChance[k.kind]}
					<fieldset class="policy-group">
						<legend>{k.label}</legend>
						<p class="group-hint">{k.hint}</p>
						<Knob label="повторная проверка каждые">
							<Stepper
								value={hours(c.probeEverySeconds)}
								unit="ч"
								atMin={c.probeEverySeconds <= 3600}
								atMax={c.probeEverySeconds >= 604800}
								busy={saving !== null}
								ondown={() => saveChance(k.kind, { probeEverySeconds: c.probeEverySeconds - 3600 })}
								onup={() => saveChance(k.kind, { probeEverySeconds: c.probeEverySeconds + 3600 })}
							/>
						</Knob>
						<Knob label="успешных проверок подряд для возврата">
							<Stepper
								value={`${c.returnClean}`}
								atMin={c.returnClean <= 2}
								atMax={c.returnClean >= 14}
								busy={saving !== null}
								ondown={() => saveChance(k.kind, { returnClean: c.returnClean - 1 })}
								onup={() => saveChance(k.kind, { returnClean: c.returnClean + 1 })}
							/>
						</Knob>
						<Knob label="неудачных проверок до списания">
							<Stepper
								value={`${c.deadFails}`}
								atMin={c.deadFails <= 1}
								atMax={c.deadFails >= 60}
								busy={saving !== null}
								ondown={() => saveChance(k.kind, { deadFails: c.deadFails - 1 })}
								onup={() => saveChance(k.kind, { deadFails: c.deadFails + 1 })}
							/>
						</Knob>
						<Knob label="списать, если не восстановился за">
							<Stepper
								value={days(c.deadAfterSeconds)}
								unit="дн"
								atMin={c.deadAfterSeconds <= 86400}
								atMax={c.deadAfterSeconds >= 31536000}
								busy={saving !== null}
								ondown={() => saveChance(k.kind, { deadAfterSeconds: c.deadAfterSeconds - 86400 })}
								onup={() => saveChance(k.kind, { deadAfterSeconds: c.deadAfterSeconds + 86400 })}
							/>
						</Knob>
					</fieldset>
				{/each}
			</Panel>

			<!-- ======== 4. what is out of the pool on suspicion now ======== -->
			<Panel title="под вопросом" sub="снято с выдачи и ждёт второго шанса; ниже — чем кончилось за 30 дней" flush>
				{#each suspects as x (x.id)}
					<Knob label="{KIND_LABEL[x.kind] ?? x.kind} {x.subject}" hint={suspectHint(x)}>
						<span class="st st-{x.status}">{x.status === 'suspect' ? 'под вопросом' : ENDED_LABEL[x.status]}</span>
					</Knob>
				{:else}
					<p class="group">нет ресурсов, исключённых из выдачи</p>
				{/each}
			</Panel>
		{/if}
	</div>
</PageShell>

<style>
	.policy-group {
		min-width: 0;
		margin: 16px;
		padding: 0 0 4px;
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.policy-group legend {
		margin: 0 auto;
		padding: 0 12px;
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
		text-align: center;
	}

	.group-hint {
		margin: 0;
		padding: 8px 16px;
		font-size: 12px;
		line-height: 1.5;
		color: var(--text-3);
	}

	.budgets {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
		gap: 20px;
	}
	.derived {
		flex-shrink: 0;
		font-size: 13px;
		color: var(--text-2);
		font-variant-numeric: tabular-nums;
	}

	.warn-line {
		margin: 0;
		padding: 10px 16px;
		font-size: 12px;
		color: var(--warn);
		border-bottom: 1px solid var(--border-subtle);
	}

	.group {
		margin: 0;
		padding: 14px 16px 8px;
		font-size: 11px;
		color: var(--text-3);
		border-bottom: 1px solid var(--border-subtle);
	}

	.st {
		flex-shrink: 0;
		font-size: 11px;
		padding: 2px 8px;
		border-radius: 10px;
		color: var(--warn);
		background: oklch(0.78 0.12 85 / 0.12);
	}

	.st-returned {
		color: var(--ok);
		background: oklch(0.72 0.15 150 / 0.12);
	}

	.st-dead,
	.st-withdrawn {
		color: var(--text-3);
		background: none;
	}

	.saved {
		font-size: 11px;
		color: var(--ok);
	}

	.seg {
		display: flex;
		flex-shrink: 0;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--inset);
		overflow: hidden;
	}

	.seg button {
		padding: 4px 10px;
		font-size: 12px;
		color: var(--text-2);
		background: none;
		border: none;
		cursor: pointer;
	}

	.seg button + button {
		border-left: 1px solid var(--border);
	}

	.seg button:hover:not(:disabled) {
		background: var(--surface);
		color: var(--text);
	}

	.seg button.on {
		background: var(--primary);
		color: #fff;
		cursor: default;
	}

	.seg button.on.spend {
		background: var(--warn);
	}

	.seg button:disabled:not(.on) {
		opacity: 0.4;
		cursor: default;
	}

	.toggle-btn {
		width: 36px;
		height: 20px;
		border-radius: 10px;
		background: var(--border);
		border: none;
		padding: 2px;
		cursor: pointer;
		flex-shrink: 0;
		transition: background 0.2s;
	}

	.toggle-btn.on {
		background: var(--primary);
	}

	.toggle-btn:disabled {
		opacity: 0.6;
		cursor: default;
	}

	.toggle-knob {
		display: block;
		width: 16px;
		height: 16px;
		border-radius: 50%;
		background: #fff;
		transform: translateX(0);
		transition: transform 0.2s;
	}

	.toggle-btn.on .toggle-knob {
		transform: translateX(16px);
	}
</style>
