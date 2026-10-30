<script lang="ts">
	import {
		CAUSE_LABEL,
		PLAYBOOK_LABEL,
		describeEntryError,
		dismissIncident,
		formatCents,
		liftCausePause,
		runIncidentRepair,
		type CausePause,
		type IncidentFeedRow,
		type PlaybookOption,
	} from '$lib/api/entry';

	// "Требуют тебя" (roadmap 42 К2 & mockup 42-incidents-mockup.html):
	// the queue, and the only acting surface on this screen.
	// Three buttons on a card — run the proposal, choose another repair,
	// close it by hand — plus the wave fuses as elements of their own.

	interface Props {
		rows: IncidentFeedRow[];
		pauses: CausePause[];
		playbooks: PlaybookOption[];
		/** Re-read the screen: every button here changes what the hub would answer. */
		onchange?: () => void;
	}

	let { rows, pauses, playbooks, onchange }: Props = $props();

	let busy = $state<string | null>(null);
	let failed = $state<Record<string, string>>({});
	let menuFor = $state<string | null>(null);

	const total = $derived(rows.length + pauses.length);

	function runnable(r: IncidentFeedRow): boolean {
		return !r.closedAt && r.status === 'open' && !!r.playbook && r.mode !== 'off';
	}

	async function act(r: IncidentFeedRow, what: 'run' | 'dismiss', playbook = '') {
		if (busy) return;
		busy = r.id;
		menuFor = null;
		try {
			if (what === 'run') await runIncidentRepair(r.id, playbook);
			else await dismissIncident(r.id);
			delete failed[r.id];
			onchange?.();
		} catch (e) {
			failed[r.id] = describeEntryError(e);
		} finally {
			busy = null;
		}
	}

	async function lift(p: CausePause) {
		if (busy) return;
		busy = p.id;
		try {
			await liftCausePause(p.cause);
			delete failed[p.id];
			onchange?.();
		} catch (e) {
			failed[p.id] = describeEntryError(e);
		} finally {
			busy = null;
		}
	}

	function ago(iso: string): string {
		const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
		if (s < 60) return 'только что';
		if (s < 3600) return `${Math.floor(s / 60)} мин`;
		if (s < 86_400) return `${Math.floor(s / 3600)} ч`;
		return `${Math.floor(s / 86_400)} дн`;
	}

	function cause(c: string): string {
		return CAUSE_LABEL[c] ?? c;
	}

	function playbookName(name: string | undefined): string {
		if (!name) return 'нет сценария ремонта';
		return PLAYBOOK_LABEL[name] ?? name;
	}

	/** The menu: every repair the hub can do, bar the one already proposed. */
	function others(r: IncidentFeedRow): PlaybookOption[] {
		return playbooks.filter((p) => p.name !== r.playbook && p.mode !== 'off');
	}
</script>

<section class="card">
	<div class="card-head">
		<h2>требуют внимания</h2>
		<span class="sub">{total}</span>
	</div>

	{#if total === 0}
		<p class="quiet">нет активных инцидентов</p>
	{:else}
		<div class="feed">
			<!-- Wave fuses first -->
			{#each pauses as p (p.id)}
				<div class="item pause">
					<span class="bar warn"></span>
					<div class="body">
						<div class="what">предохранитель волны: {cause(p.cause)} на паузе</div>
						<div class="why">{p.reason}</div>
						<div class="when">
							{ago(p.pausedAt)} · ждёт · предложено: разобрать инциденты, потом снять паузу
							{#if p.boxes}
								· машин в волне: {p.boxes}{/if} · авто-ремонт этой причины встал
						</div>
						{#if failed[p.id]}
							<div class="failed">{failed[p.id]}</div>
						{/if}
						<div class="acts">
							<button class="act do" disabled={busy === p.id} onclick={() => lift(p)}>
								{busy === p.id ? 'снимаю…' : 'снять паузу'}
							</button>
							<a class="act" href="/censor">открыть censor</a>
						</div>
					</div>
				</div>
			{/each}

			<!-- Queued incidents -->
			{#each rows as r (r.id)}
				<div class="item">
					<span class="bar" class:warn={!r.spends}></span>
					<div class="body">
						<div class="what">{r.box}: {cause(r.cause)}</div>
						<div class="why">{r.advice}</div>
						<div class="when">
							{ago(r.openedAt)} · ждёт
							{#if r.status === 'acting'}
								· идёт: {playbookName(r.playbook)}
							{:else}
								· предложено: {playbookName(r.playbook)}{r.spends ? ', тратит деньги' : ''}
							{/if}
							{#if r.repairs > 0}
								&nbsp;· попыток: {r.repairs}{r.repairsFailed ? `, неудачных: ${r.repairsFailed}` : ''}
							{/if}
							{#if r.costCents > 0}
								&nbsp;· {formatCents(r.costCents)}
							{/if}
						</div>
						{#if r.handoff}
							<div class="handoff">остановка автопилота: {r.handoff}</div>
						{/if}
						{#if failed[r.id]}
							<div class="failed">{failed[r.id]}</div>
						{/if}

						<div class="acts">
							{#if runnable(r)}
								<button class="act do" disabled={busy === r.id} onclick={() => act(r, 'run')}>
									{busy === r.id ? 'запускаю…' : `выполнить: ${playbookName(r.playbook)}`}
								</button>
							{/if}
							<button
								class="act"
								disabled={busy === r.id || others(r).length === 0}
								onclick={() => (menuFor = menuFor === r.id ? null : r.id)}
							>
								другое действие
							</button>
							<button class="act dismiss" disabled={busy === r.id} onclick={() => act(r, 'dismiss')}>
								закрыть руками
							</button>
							<a class="act" href="/entry">открыть машину</a>
						</div>

						{#if menuFor === r.id}
							<div class="menu">
								<p class="menu-head">варианты действия</p>
								{#each others(r) as p (p.name)}
									<button class="choice" disabled={busy === r.id} onclick={() => act(r, 'run', p.name)}>
										<span class="choice-name">{playbookName(p.name)}</span>
										<span class="choice-why">{p.why}{p.spends ? ' · тратит деньги' : ''}</span>
									</button>
								{/each}
							</div>
						{/if}
					</div>
				</div>
			{/each}
		</div>
	{/if}
</section>

<style>
	.card {
		background: var(--card-bg, oklch(0.2 0 0));
		border: 1px solid var(--border-subtle, oklch(0.19 0 0));
		border-radius: 8px;
		margin-bottom: 16px;
	}

	.card-head {
		display: flex;
		align-items: baseline;
		gap: 10px;
		padding: 13px 16px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
	}

	.card-head h2 {
		margin: 0;
		font-size: 13px;
		font-weight: 600;
		color: var(--text, oklch(0.93 0 0));
	}

	.card-head .sub {
		font-size: 11px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.quiet {
		margin: 0;
		padding: 22px 16px;
		font-size: 12px;
		color: var(--text-3, oklch(0.42 0 0));
		text-align: center;
	}

	.item {
		position: relative;
		display: flex;
		gap: 11px;
		padding: 13px 16px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
	}

	.item:last-child {
		border-bottom: none;
	}

	.item.pause {
		background: color-mix(in oklab, var(--warn, oklch(0.78 0.12 85)) 4%, transparent);
	}

	.item .bar {
		width: 2px;
		border-radius: 1px;
		flex-shrink: 0;
		background: var(--bad, oklch(0.65 0.2 25));
	}

	.item .bar.warn {
		background: var(--warn, oklch(0.78 0.12 85));
	}

	.item .body {
		min-width: 0;
		flex: 1;
	}

	.item .what {
		font-size: 12px;
		font-weight: 600;
		color: var(--text, oklch(0.93 0 0));
	}

	.item .why {
		margin-top: 2px;
		font-size: 11px;
		color: var(--text-2, oklch(0.62 0 0));
		line-height: 1.45;
	}

	.item .when {
		margin-top: 5px;
		font-family: var(--mono, monospace);
		font-variant-numeric: tabular-nums;
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.handoff {
		margin-top: 6px;
		font-size: 11px;
		color: var(--warn, oklch(0.78 0.12 85));
	}

	.failed {
		margin-top: 6px;
		font-size: 11px;
		color: var(--bad, oklch(0.65 0.2 25));
	}

	.acts {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 7px;
		margin-top: 9px;
	}

	.act {
		display: inline-flex;
		align-items: center;
		padding: 5px 11px;
		border-radius: 6px;
		font-size: 11px;
		font-family: var(--sans, inherit);
		border: 1px solid var(--border, oklch(0.24 0 0));
		color: var(--text-2, oklch(0.62 0 0));
		background: none;
		text-decoration: none;
		cursor: pointer;
		transition:
			background 0.12s,
			color 0.12s,
			border-color 0.12s;
	}

	.act:hover:not(:disabled) {
		background: var(--surface, oklch(0.23 0 0));
		color: var(--text, oklch(0.93 0 0));
	}

	.act.do {
		color: var(--primary, oklch(0.75 0.14 350));
		border-color: oklch(0.75 0.14 350 / 0.4);
	}

	.act.do:hover:not(:disabled) {
		background: var(--primary-subtle, oklch(0.75 0.14 350 / 0.1));
	}

	.act.dismiss {
		margin-left: auto;
		color: var(--text-3, oklch(0.42 0 0));
		border-color: transparent;
	}

	.act.dismiss:hover:not(:disabled) {
		color: var(--text-2, oklch(0.62 0 0));
		background: var(--surface, oklch(0.23 0 0));
	}

	.act:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.menu {
		margin-top: 9px;
		border: 1px solid var(--border-subtle, oklch(0.19 0 0));
		border-radius: 7px;
		overflow: hidden;
		background: var(--inset, oklch(0.17 0 0));
	}

	.menu-head {
		margin: 0;
		padding: 7px 11px;
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
		background: var(--surface, oklch(0.23 0 0));
	}

	.choice {
		display: block;
		width: 100%;
		padding: 8px 11px;
		text-align: left;
		background: none;
		border: none;
		border-top: 1px solid var(--border-subtle, oklch(0.19 0 0));
		cursor: pointer;
	}

	.choice:hover:not(:disabled) {
		background: var(--surface, oklch(0.23 0 0));
	}

	.choice:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.choice-name {
		display: block;
		font-size: 11px;
		font-weight: 500;
		color: var(--text, oklch(0.93 0 0));
	}

	.choice-why {
		display: block;
		margin-top: 2px;
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
	}
</style>
