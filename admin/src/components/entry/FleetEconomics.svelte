<script lang="ts">
	import { formatCents, type EntryProviderCompare, type ProviderStats } from '$lib/api/entry';

	// What each hoster's machines cost and carried, from the provider
	// comparison (internal/admin/entry_providers.go). Every figure here is
	// measured over the comparison's window: the traffic from the relays' own
	// pulses, the spend from the rent accrued while the machines were in
	// service. A plan's traffic allowance is a ceiling, not a measurement, and
	// the table used to divide a made-up allowance per hoster into the price.
	interface Props {
		compare: EntryProviderCompare | null;
	}

	let { compare }: Props = $props();

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

	/** What a terabyte actually carried cost: the accrued rent over the measured traffic. */
	function costPerTb(h: ProviderStats): string | null {
		if (h.trafficGb === null || h.trafficGb <= 0 || h.spentCents <= 0) return null;
		return formatCents(Math.round(h.spentCents / (h.trafficGb / 1000)));
	}
</script>

<section class="card">
	<div class="card-head">
		<h2>провайдеры и себестоимость</h2>
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
							title="{h.provider}: {h.aliveCount} шт ({pct}%)"
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
						<th class="num">трафик за {compare?.windowDays ?? 30} дн</th>
						<th class="num">себес / тб</th>
						<th class="num">жизнь ip до бана</th>
					</tr>
				</thead>
				<tbody>
					{#each hosters as h, i (h.provider)}
						{@const pct = sharePct(h.aliveCount)}
						<tr>
							<td class="col-prov">
								<div class="prov-name-cell">
									<span class="dot" style="background-color: {providerColor(h.provider, i)}"></span>
									<span class="pname m">{h.provider}</span>
								</div>
							</td>
							<td class="num m">{h.aliveCount}</td>
							<td class="num m">{pct}%</td>
							<td class="num m">{formatCents(h.monthlyCents)}</td>
							<td class="num m">
								{#if h.perBoxMonthlyCents !== null}
									{formatCents(h.perBoxMonthlyCents)}
								{:else}
									<span class="muted">—</span>
								{/if}
							</td>
							<td class="num m">
								{#if h.trafficGb !== null}
									{formatTraffic(h.trafficGb)}
								{:else}
									<span class="muted">—</span>
								{/if}
							</td>
							<td class="num m">
								{#if costPerTb(h) !== null}
									<span class="cost-val">{costPerTb(h)}</span>
								{:else}
									<span class="muted">—</span>
								{/if}
							</td>
							<td class="num m">
								{#if h.medianLifeDays !== null}
									{h.medianLifeDays < 2 ? `${Math.round(h.medianLifeDays * 24)} ч` : `${h.medianLifeDays.toFixed(1)} дн`}
								{:else}
									<span class="muted">—</span>
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
