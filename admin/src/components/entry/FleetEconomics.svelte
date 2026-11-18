<script lang="ts">
	import { describeApiError } from '$lib/api/client';
	import {
		formatCents,
		setHosterPlanTb,
		type EntryProviderCompare,
		type ProviderStats,
		providerLabel,
	} from '$lib/api/entry';

	// What each hoster's machines cost and carried, from the provider
	// comparison (internal/admin/entry_providers.go). Every figure here is
	// measured over the comparison's window: the traffic from the relays' own
	// pulses, the spend from the rent accrued while the machines were in
	// service. A plan's traffic allowance is a ceiling, not a measurement, and
	// the table used to divide a made-up allowance per hoster into the price.
	//
	// The owner may type the terabytes a plan includes (18.11): with them the
	// cost of a terabyte is a machine's rent over its plan, the figure a hoster
	// is chosen by. Without them it stays the measured one, which three testers
	// carrying a few gigabytes turn into thousands of dollars.
	interface Props {
		compare: EntryProviderCompare | null;
		/** a plan changed here: the fleet is read again */
		onChanged?: () => void;
	}

	let { compare, onChanged }: Props = $props();

	let editing = $state<string | null>(null);
	let draft = $state('');
	let planError = $state<string | null>(null);
	/** plans saved here before the next answer carries them; 0 is forgotten */
	let saved = $state<Record<string, number>>({});

	function planOf(h: ProviderStats): number | null {
		const s = saved[h.provider];
		if (s !== undefined) return s > 0 ? s : null;
		return h.planTb;
	}

	function startPlan(h: ProviderStats) {
		planError = null;
		draft = planOf(h)?.toString() ?? '';
		editing = h.provider;
	}

	async function finishPlan(h: ProviderStats, keep: boolean) {
		if (editing !== h.provider) return;
		editing = null;
		if (!keep) return;
		const text = draft
			.trim()
			.replace(',', '.')
			.replace(/\s*тб$/i, '');
		const tb = text === '' ? 0 : Number(text);
		if (!Number.isFinite(tb) || tb < 0) {
			planError = `${providerLabel(h.provider)}: «${draft}» не число терабайт`;
			return;
		}
		if (tb === (planOf(h) ?? 0)) return;
		try {
			await setHosterPlanTb(h.provider, tb);
			saved = { ...saved, [h.provider]: tb };
			onChanged?.();
		} catch (e) {
			planError = `${providerLabel(h.provider)}: ${describeApiError(e)}`;
		}
	}

	function planKey(e: KeyboardEvent, h: ProviderStats) {
		if (e.key === 'Enter') finishPlan(h, true);
		else if (e.key === 'Escape') finishPlan(h, false);
	}

	function focusSelect(node: HTMLInputElement) {
		node.focus();
		node.select();
	}

	const PROVIDER_COLORS: Record<string, string> = {
		vultr: '#ec4899',
		vdsina: '#ff6600',
		hetzner: '#06b6d4',
		digitalocean: '#3b82f6',
		openstack: '#ed1944',
		linode: '#10b981',
		scaleway: '#8b5cf6',
		ovh: '#f59e0b',
		aws: '#f97316',
	};

	function providerColor(name: string, index: number): string {
		const key = name.toLowerCase();
		if (PROVIDER_COLORS[key]) return PROVIDER_COLORS[key];
		const palette = ['#ec4899', '#06b6d4', '#10b981', '#f59e0b', '#8b5cf6', '#3b82f6', '#14b8a6'];
		return palette[index % palette.length];
	}

	const hosters = $derived(
		[...(compare?.hosting ?? [])]
			.filter((h) => h.aliveCount > 0 || h.boughtCount > 0)
			.sort((a, b) => b.aliveCount - a.aliveCount || a.provider.localeCompare(b.provider)),
	);

	const totalAlive = $derived(hosters.reduce((acc, h) => acc + h.aliveCount, 0));

	function sharePct(alive: number): number {
		if (totalAlive === 0) return 0;
		return Math.round((alive / totalAlive) * 100);
	}

	function formatTraffic(gb: number): string {
		if (gb >= 1000) return `${(gb / 1000).toFixed(1)} ТБ`;
		return `${gb < 10 ? gb.toFixed(2) : gb.toFixed(0)} ГБ`;
	}

	/**
	 * What a terabyte costs: one machine's rent over its plan's terabytes when
	 * the owner gave them, else the accrued rent over the measured traffic.
	 */
	function costPerTb(h: ProviderStats): { text: string; byPlan: boolean } | null {
		const plan = planOf(h);
		if (plan !== null && h.perBoxMonthlyCents !== null) {
			return { text: formatCents(Math.round(h.perBoxMonthlyCents / plan)), byPlan: true };
		}
		if (h.trafficGb === null || h.trafficGb <= 0 || h.spentCents <= 0) return null;
		return { text: formatCents(Math.round(h.spentCents / (h.trafficGb / 1000))), byPlan: false };
	}
</script>

<section class="card">
	<div class="card-head">
		<h2>провайдеры и себестоимость</h2>
		{#if planError}<span class="plan-err" role="alert">{planError}</span>{/if}
	</div>

	<!-- Allocation bar -->
	{#if hosters.length > 0}
		<div class="alloc-wrap">
			<div class="alloc-bar" role="progressbar" aria-label="Распределение машин по провайдерам">
				{#each hosters as h, i (h.provider)}
					{@const pct = sharePct(h.aliveCount)}
					{#if pct > 0}
						<div
							class="alloc-seg"
							style="width: {pct}%; background-color: {providerColor(h.provider, i)}"
							title="{providerLabel(h.provider)}: {h.aliveCount} шт ({pct}%)"
						></div>
					{/if}
				{/each}
			</div>
		</div>
	{/if}

	<!-- Comparative Unit Economics Table -->
	{#if hosters.length === 0}
		<div class="empty-hint">нет активных хостинг-провайдеров в базе</div>
	{:else}
		<div class="table-wrap">
			<table class="econ-table">
				<thead>
					<tr>
						<th>провайдер</th>
						<th class="num">машины</th>
						<th class="num">доля</th>
						<th class="num">аренда / мес</th>
						<th class="num">тариф / мес</th>
						<th class="num">использовано за {compare?.windowDays ?? 30} дн</th>
						<th class="num" title="сколько ТБ в месяц входит в тариф одной машины; двойной клик по ячейке: вписать">
							тб в тарифе
						</th>
						<th class="num">себес / тб</th>
						<th class="num">жизнь ip до бана</th>
					</tr>
				</thead>
				<tbody>
					{#each hosters as h, i (h.provider)}
						{@const pct = sharePct(h.aliveCount)}
						{@const cost = costPerTb(h)}
						{@const planTb = planOf(h)}
						<tr>
							<td class="col-prov">
								<div class="prov-name-cell">
									<span class="dot" style="background-color: {providerColor(h.provider, i)}"></span>
									<span class="pname m">{providerLabel(h.provider)}</span>
								</div>
							</td>
							<td class="num m">{h.aliveCount}</td>
							<td class="num m">{pct}%</td>
							<td class="num m">{formatCents(h.monthlyCents)}</td>
							<td class="num m">
								{#if h.perBoxMonthlyCents !== null}
									{formatCents(h.perBoxMonthlyCents)}
								{:else}
									<span class="muted">-</span>
								{/if}
							</td>
							<td class="num m">
								{#if planTb !== null && h.aliveCount > 0}
									<span
										title="за {compare?.windowDays ??
											30} дн прошло через машины провайдера / {planTb} ТБ в тарифе × {h.aliveCount} маш."
									>
										{formatTraffic(h.trafficGb ?? 0)}
										<span class="muted">из {formatTraffic(planTb * 1000 * h.aliveCount)}</span>
									</span>
								{:else if h.trafficGb !== null}
									{formatTraffic(h.trafficGb)}
								{:else}
									<span class="muted">-</span>
								{/if}
							</td>
							<td class="num m">
								{#if editing === h.provider}
									<input
										class="plan-input m"
										placeholder="32"
										aria-label="тб в тарифе у {providerLabel(h.provider)}"
										bind:value={draft}
										use:focusSelect
										onkeydown={(e) => planKey(e, h)}
										onblur={() => finishPlan(h, true)}
									/>
								{:else}
									{@const plan = planOf(h)}
									<button
										class="plan-btn m"
										ondblclick={() => startPlan(h)}
										title="двойной клик: сколько ТБ в месяц входит в тариф одной машины, пусто или 0 стирает"
									>
										{#if plan !== null}{plan} ТБ{:else}<span class="muted">-</span>{/if}
									</button>
								{/if}
							</td>
							<td class="num m">
								{#if cost !== null}
									<span
										class="cost-val"
										class:measured={!cost.byPlan}
										title={cost.byPlan
											? 'тариф одной машины / тб в тарифе'
											: 'аренда за окно / трафик за окно; впиши тб в тарифе, чтобы считать по тарифу'}>{cost.text}</span
									>
								{:else}
									<span class="muted">-</span>
								{/if}
							</td>
							<td class="num m">
								{#if h.medianLifeDays !== null}
									{h.medianLifeDays < 2
										? `${Math.round(h.medianLifeDays * 24)} ч`
										: `${h.medianLifeDays.toFixed(1)} дн`}
								{:else}
									<span class="muted">-</span>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</section>

<style>
	.plan-btn {
		padding: 0;
		font: inherit;
		color: inherit;
		background: none;
		border: none;
		cursor: text;
	}

	.plan-btn:hover {
		color: var(--primary, oklch(0.75 0.14 350));
	}

	.plan-input {
		width: 8ch;
		padding: 1px 4px;
		font: inherit;
		text-align: right;
		color: var(--text, oklch(0.93 0 0));
		background: var(--surface-2, oklch(0.22 0 0));
		border: 1px solid var(--primary, oklch(0.75 0.14 350));
		border-radius: 4px;
		outline: none;
	}

	.plan-err {
		font-size: 11px;
		color: var(--bad, oklch(0.7 0.18 25));
	}

	.cost-val.measured {
		color: var(--text-3, oklch(0.42 0 0));
	}

	.card {
		background: var(--card-bg, oklch(0.2 0 0));
		border: 1px solid var(--border-subtle, oklch(0.19 0 0));
		border-radius: 8px;
		margin-bottom: 16px;
		overflow: hidden;
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

	/* Allocation bar */
	.alloc-wrap {
		padding: 14px 16px 12px;
	}

	.alloc-bar {
		display: flex;
		width: 100%;
		height: 8px;
		border-radius: 4px;
		overflow: hidden;
		background: var(--border-subtle, oklch(0.2 0 0));
	}

	.alloc-seg {
		height: 100%;
		transition: opacity 0.15s ease;
		cursor: pointer;
	}

	.alloc-seg:hover {
		opacity: 0.85;
	}

	/* Table */
	.table-wrap {
		overflow-x: auto;
	}

	.econ-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 13px;
		text-align: left;
	}

	.econ-table th {
		padding: 8px 16px;
		color: var(--text-3, oklch(0.5 0 0));
		font-weight: 500;
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		border-bottom: 1px solid var(--border-subtle, oklch(0.2 0 0));
		white-space: nowrap;
	}

	.econ-table td {
		padding: 10px 16px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.18 0 0));
		vertical-align: middle;
		white-space: nowrap;
	}

	.econ-table tr:last-child td {
		border-bottom: none;
	}

	.econ-table tr:hover {
		background: rgba(255, 255, 255, 0.02);
	}

	.col-prov {
		font-weight: 500;
	}

	.prov-name-cell {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.pname {
		font-size: 13px;
		color: var(--text, oklch(0.95 0 0));
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.num {
		text-align: right;
		font-family: var(--mono, monospace);
		font-variant-numeric: tabular-nums;
	}

	.cost-val {
		color: var(--ok, oklch(0.72 0.15 155));
		font-weight: 600;
	}

	.muted {
		color: var(--text-3, oklch(0.42 0 0));
	}

	.empty-hint {
		padding: 24px 20px;
		font-size: 13px;
		color: var(--text-3, oklch(0.5 0 0));
		text-align: center;
	}
</style>
