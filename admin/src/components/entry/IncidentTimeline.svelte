<script lang="ts">
	import {
		CAUSE_LABEL,
		PLAYBOOK_LABEL,
		describeEntryError,
		fetchIncidentRepairs,
		formatCents,
		formatSeconds,
		type IncidentFeedRow,
		type IncidentRepair,
		type IncidentResolution,
	} from '$lib/api/entry';

	// The feed (roadmap 42 К2 & mockup 42-incidents-mockup.html):
	// every incident of the window, newest first, and one of them open at a
	// time with its 4-stage timeline and the evidence it rested on.

	interface Props {
		rows: IncidentFeedRow[];
		/** Which window the feed covers, for the heading. */
		window: string;
	}

	let { rows, window }: Props = $props();

	type Filter = 'all' | 'handed' | 'failed';

	const FILTERS: { id: Filter; label: string }[] = [
		{ id: 'all', label: 'все' },
		{ id: 'handed', label: 'требует оператора' },
		{ id: 'failed', label: 'ошибка ремонта' },
	];

	// The operator opens this screen because something wants them, not to
	// watch the autopilot work: the feed starts on the rows that stopped at a
	// person, and falls back to everything when there are none.
	let filter = $state<Filter>(
		rows.some((r) => r.resolution === 'handed' || r.resolution === 'working') ? 'handed' : 'all',
	);
	let openId = $state<string | null>(null);
	let repairs = $state<Record<string, IncidentRepair[]>>({});
	let repairError = $state<Record<string, string>>({});

	const shown = $derived.by(() => {
		if (filter === 'all') return rows;
		if (filter === 'handed') return rows.filter((r) => r.resolution === 'handed' || r.resolution === 'working');
		if (filter === 'failed') return rows.filter((r) => r.resolution === 'failed');
		return rows;
	});

	async function toggle(r: IncidentFeedRow) {
		openId = openId === r.id ? null : r.id;
		if (openId !== r.id || repairs[r.id]) return;
		try {
			const { repairs: list } = await fetchIncidentRepairs(r.id);
			repairs[r.id] = list;
			delete repairError[r.id];
		} catch (e) {
			repairError[r.id] = describeEntryError(e);
		}
	}

	function cause(c: string): string {
		return CAUSE_LABEL[c] ?? c;
	}

	function playbookName(name: string | undefined): string {
		if (!name) return 'ремонта нет';
		return PLAYBOOK_LABEL[name] ?? name;
	}

	function ago(iso: string): string {
		const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
		if (s < 60) return 'только что';
		if (s < 3600) return `${Math.max(1, Math.floor(s / 60))} мин`;
		if (s < 86_400) return `${Math.floor(s / 3600)} ч`;
		return `${Math.floor(s / 86_400)} дн`;
	}

	function span(r: IncidentFeedRow): string {
		const end = r.admittedAt ?? r.closedAt;
		if (!end) return 'идёт';
		const sec = (new Date(end).getTime() - new Date(r.openedAt).getTime()) / 1000;
		return formatSeconds(sec);
	}

	interface StageItem {
		id: string;
		label: string;
		delta: string;
		text: string;
		dotClass: string;
	}

	function getStages(r: IncidentFeedRow): StageItem[] {
		const t0 = new Date(r.openedAt).getTime();
		const delta = (iso: string) => formatSeconds((new Date(iso).getTime() - t0) / 1000);

		const out: StageItem[] = [
			{
				id: 'opened',
				label: 'провал',
				delta: '0 с',
				text: `${cause(r.cause)}${r.advice ? ` — ${r.advice}` : ''}`,
				dotClass: '',
			},
		];

		if (r.actedAt) {
			const cost = r.costCents > 0 ? ` · ${formatCents(r.costCents)}` : '';
			out.push({
				id: 'acted',
				label: 'ремонт',
				delta: `+${delta(r.actedAt)}`,
				text: `плейбук ${playbookName(r.playbook)}${r.decidedBy ? ` · решение: ${r.decidedBy}` : ''}${cost}`,
				dotClass: '',
			});
		}

		if (r.admittedAt) {
			out.push({
				id: 'admitted',
				label: 'допуск',
				delta: `+${delta(r.admittedAt)}`,
				text: 'замена прошла пробу и вошла в пул',
				dotClass: '',
			});
		} else if (r.closedAt) {
			out.push({
				id: 'closed',
				label: 'закрыт',
				delta: `+${delta(r.closedAt)}`,
				text: r.outcome || 'закрыт',
				dotClass: r.resolution === 'failed' ? 'bad' : r.resolution === 'handed' ? 'warn' : '',
			});
		}

		return out;
	}

	function tagClass(res: IncidentResolution): string {
		switch (res) {
			case 'auto':
				return 'tag-auto';
			case 'handed':
			case 'working':
				return 'tag-human';
			case 'failed':
				return 'tag-failed';
			case 'dismissed':
			default:
				return 'tag-dismiss';
		}
	}

	function tagLabel(res: IncidentResolution): string {
		switch (res) {
			case 'auto':
				return 'автопилот';
			case 'handed':
				return 'оператор';
			case 'failed':
				return 'ошибка ремонта';
			case 'dismissed':
				return 'отклонено';
			case 'working':
				return 'в работе';
			default:
				return res;
		}
	}

	function evidenceList(r: IncidentFeedRow): { key: string; val: string }[] {
		return Object.entries(r.evidence ?? {})
			.sort(([a], [b]) => a.localeCompare(b))
			.map(([key, val]) => ({ key, val }));
	}

	function formatVerdict(val: string): string {
		if (!val) return '—';
		return val.replace(/✓/g, '<span class="y">✓</span>').replace(/✗/g, '<span class="n">✗</span>');
	}
</script>

<section class="card">
	<div class="card-head">
		<h2>лента</h2>
		<span class="sub">{rows.length} за {window}</span>
		<div class="right">
			{#each FILTERS as f (f.id)}
				<button class="chip" class:on={filter === f.id} onclick={() => (filter = f.id)}>
					{f.label}
				</button>
			{/each}
		</div>
	</div>

	{#if shown.length === 0}
		<p class="quiet">
			{rows.length === 0 ? 'нет инцидентов' : 'нет инцидентов под выбранный фильтр'}
		</p>
	{:else}
		<div class="ledger">
			{#each shown as r (r.id)}
				<div class="inc">
					<div
						class="inc-row"
						role="button"
						tabindex="0"
						onclick={() => toggle(r)}
						onkeydown={(e) => e.key === 'Enter' && toggle(r)}
					>
						<span class="caret">{openId === r.id ? '−' : '+'}</span>
						<span class="ibox">{r.box}</span>
						<span class="icause">{cause(r.cause)}</span>
						<span class="ioutcome">{r.outcome || r.advice}</span>
						<span class="tag {tagClass(r.resolution)}">{tagLabel(r.resolution)}</span>
						<span class="iwhen">{ago(r.openedAt)}</span>
					</div>

					{#if openId === r.id}
						<div class="inc-body">
							<!-- 4-stage connected timeline -->
							<div class="timeline">
								{#each getStages(r) as s (s.id)}
									<div class="stage">
										<div class="dot {s.dotClass}"></div>
										<div class="s-name">{s.label}</div>
										<div class="s-time">{s.delta}</div>
										<div class="s-detail">{s.text}</div>
									</div>
								{/each}
							</div>

							<div class="span-info">
								целиком: {span(r)}
								{#if r.repairs > 0}
									&nbsp;· попыток ремонта: {r.repairs}{r.repairsFailed ? `, из них неудачных ${r.repairsFailed}` : ''}
								{/if}
								{#if r.handoff}&nbsp;· остановка: {r.handoff}{/if}
							</div>

							<!-- Evidence snapshot -->
							{#if evidenceList(r).length > 0}
								<div class="evidence">
									<div class="ev-head">вердикты точек наблюдения на момент диагноза</div>
									<div class="ev-grid">
										<span class="gh">релей / источник</span>
										<span class="gh">сигнал / вердикт</span>
										{#each evidenceList(r) as item (item.key)}
											<span class="gc rname">{item.key}</span>
											<span class="gc sig">{@html formatVerdict(item.val)}</span>
										{/each}
									</div>
									{#if r.advice}
										<div class="ev-verdict">
											<b>диагноз:</b>
											{r.advice}
										</div>
									{/if}
								</div>
							{:else}
								<p class="ev-empty">улик нет: инцидент заведён не по пробам (смерть релея, сетка не отвечала)</p>
							{/if}

							<!-- Repairs attempts history -->
							{#if repairError[r.id]}
								<p class="err">{repairError[r.id]}</p>
							{:else if repairs[r.id]?.length}
								<div class="tries">
									<div class="tries-head">что пробовали</div>
									{#each repairs[r.id] as t (t.id)}
										<div class="try">
											<span class="try-name">{playbookName(t.playbook)}</span>
											<span class="try-status" class:bad={t.status === 'failed'}>{t.status}</span>
											<span class="try-who">{t.decidedBy}</span>
											{#if t.error}<span class="try-err">{t.error}</span>{/if}
										</div>
									{/each}
								</div>
							{/if}
						</div>
					{/if}
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

	.card-head .right {
		margin-left: auto;
		display: flex;
		gap: 6px;
	}

	.chip {
		padding: 4px 11px;
		border-radius: 6px;
		font-size: 11px;
		font-family: var(--sans, inherit);
		color: var(--text-2, oklch(0.62 0 0));
		border: 1px solid var(--border, oklch(0.24 0 0));
		background: none;
		white-space: nowrap;
		cursor: pointer;
		transition:
			background 0.12s,
			color 0.12s,
			border-color 0.12s;
	}

	.chip:hover {
		background: var(--surface, oklch(0.23 0 0));
	}

	.chip.on {
		color: var(--text, oklch(0.93 0 0));
		background: var(--primary-subtle, oklch(0.75 0.14 350 / 0.1));
		border-color: oklch(0.75 0.14 350 / 0.35);
	}

	.quiet {
		margin: 0;
		padding: 22px 16px;
		font-size: 12px;
		color: var(--text-3, oklch(0.42 0 0));
		text-align: center;
	}

	.inc {
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
	}

	.inc:last-child {
		border-bottom: none;
	}

	.inc-row {
		display: grid;
		grid-template-columns: 18px 90px 1fr 180px 120px 70px;
		align-items: center;
		gap: 12px;
		padding: 11px 16px;
		cursor: pointer;
		user-select: none;
		transition: background 0.1s;
	}

	.inc-row:hover {
		background: var(--inset, oklch(0.17 0 0));
	}

	.caret {
		font-family: var(--mono, monospace);
		color: var(--text-3, oklch(0.42 0 0));
		font-size: 11px;
	}

	.ibox {
		font-family: var(--mono, monospace);
		font-size: 12px;
		font-weight: 600;
		color: var(--text, oklch(0.93 0 0));
	}

	.icause {
		font-size: 12px;
		color: var(--text, oklch(0.93 0 0));
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.ioutcome {
		font-size: 11px;
		color: var(--text-2, oklch(0.62 0 0));
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.tag {
		padding: 2px 8px;
		border-radius: 4px;
		font-size: 10px;
		white-space: nowrap;
		width: fit-content;
	}

	.tag-auto {
		color: var(--ok, oklch(0.72 0.15 155));
		background: oklch(0.72 0.15 155 / 0.12);
	}

	.tag-human {
		color: var(--warn, oklch(0.78 0.12 85));
		background: oklch(0.78 0.12 85 / 0.12);
	}

	.tag-dismiss {
		color: var(--text-3, oklch(0.42 0 0));
		background: oklch(0.52 0 0 / 0.12);
	}

	.tag-failed {
		color: var(--bad, oklch(0.65 0.2 25));
		background: oklch(0.65 0.2 25 / 0.12);
	}

	.iwhen {
		font-family: var(--mono, monospace);
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
		text-align: right;
	}

	/* Expanded body */
	.inc-body {
		padding: 4px 16px 18px 46px;
		background: var(--card-bg, oklch(0.2 0 0));
	}

	.timeline {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
		gap: 0;
		margin: 8px 0 16px;
	}

	.stage {
		position: relative;
		padding: 0 14px 0 0;
	}

	.stage .dot {
		width: 11px;
		height: 11px;
		border-radius: 50%;
		background: var(--ok, oklch(0.72 0.15 155));
		border: 2px solid var(--card-bg, oklch(0.2 0 0));
		position: relative;
		z-index: 1;
	}

	.stage .dot.warn {
		background: var(--warn, oklch(0.78 0.12 85));
	}

	.stage .dot.bad {
		background: var(--bad, oklch(0.65 0.2 25));
	}

	.stage::before {
		content: '';
		position: absolute;
		top: 5px;
		left: 11px;
		right: 0;
		height: 2px;
		background: var(--border, oklch(0.24 0 0));
	}

	.stage:last-child::before {
		display: none;
	}

	.stage .s-name {
		margin-top: 8px;
		font-size: 11px;
		font-weight: 600;
		color: var(--text, oklch(0.93 0 0));
	}

	.stage .s-time {
		font-family: var(--mono, monospace);
		font-variant-numeric: tabular-nums;
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
		margin-top: 1px;
	}

	.stage .s-detail {
		margin-top: 4px;
		font-size: 11px;
		color: var(--text-2, oklch(0.62 0 0));
		line-height: 1.4;
		padding-right: 10px;
	}

	.span-info {
		margin: 4px 0 12px;
		font-family: var(--mono, monospace);
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	/* Evidence grid */
	.evidence {
		border: 1px solid var(--border-subtle, oklch(0.19 0 0));
		border-radius: 7px;
		background: var(--inset, oklch(0.17 0 0));
		overflow: hidden;
		margin-top: 8px;
	}

	.ev-head {
		padding: 8px 12px;
		font-size: 11px;
		color: var(--text-3, oklch(0.42 0 0));
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
	}

	.ev-grid {
		display: grid;
		grid-template-columns: 180px 1fr;
		font-size: 11px;
	}

	.ev-grid .gh {
		padding: 6px 12px;
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
	}

	.ev-grid .gc {
		padding: 7px 12px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
		font-family: var(--mono, monospace);
	}

	.ev-grid .gc.rname {
		color: var(--text, oklch(0.93 0 0));
	}

	.ev-grid .gc.sig {
		color: var(--text-2, oklch(0.62 0 0));
	}

	:global(.ev-grid .y) {
		color: var(--ok, oklch(0.72 0.15 155));
		font-weight: 600;
	}

	:global(.ev-grid .n) {
		color: var(--bad, oklch(0.65 0.2 25));
		font-weight: 600;
	}

	.ev-verdict {
		padding: 8px 12px;
		font-size: 11px;
		color: var(--text-2, oklch(0.62 0 0));
		line-height: 1.45;
	}

	.ev-verdict b {
		color: var(--text, oklch(0.93 0 0));
	}

	.ev-empty {
		margin-top: 8px;
		font-size: 11px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	/* Tries */
	.tries {
		margin-top: 10px;
		border: 1px solid var(--border-subtle, oklch(0.19 0 0));
		border-radius: 7px;
		overflow: hidden;
		background: var(--inset, oklch(0.17 0 0));
	}

	.tries-head {
		padding: 7px 11px;
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
		background: var(--surface, oklch(0.23 0 0));
	}

	.try {
		display: flex;
		gap: 12px;
		padding: 6px 11px;
		border-top: 1px solid var(--border-subtle, oklch(0.19 0 0));
		font-family: var(--mono, monospace);
		font-size: 10px;
	}

	.try-name {
		min-width: 150px;
		font-family: var(--sans, inherit);
		color: var(--text-2, oklch(0.62 0 0));
	}

	.try-status {
		min-width: 54px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.try-status.bad {
		color: var(--bad, oklch(0.65 0.2 25));
	}

	.try-who,
	.try-err {
		color: var(--text-3, oklch(0.42 0 0));
	}

	.err {
		margin: 8px 0 0;
		font-size: 11px;
		color: var(--bad, oklch(0.65 0.2 25));
	}

	@media (max-width: 900px) {
		.inc-row {
			grid-template-columns: 18px 70px 1fr auto;
		}

		.ioutcome,
		.iwhen {
			display: none;
		}
	}
</style>
