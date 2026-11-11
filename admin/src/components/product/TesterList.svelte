<script lang="ts">
	import { onMount } from 'svelte';
	import { SvelteSet } from 'svelte/reactivity';
	import TrendChart, { type TrendMark } from '$components/entry/TrendChart.svelte';
	import { describeApiError } from '$lib/api/client';
	import type { TesterCharts, TesterLines } from '$lib/api/product';
	import {
		fetchTesterInstalls,
		fetchTesterRelays,
		renameTester,
		type TesterInstall,
		type TesterRelays,
	} from '$lib/api/support';
	import { ago } from '$lib/product/format';

	// Testers are the hub's users today: installs of the client in tester mode,
	// not subscriptions sold or issued, so they come from the tester stream.
	// One row each, the last bucket of both lines beside the name; a tap opens
	// the two charts, another closes them.
	let { charts = null }: { charts?: TesterCharts | null } = $props();

	let installs = $state<TesterInstall[]>([]);
	let rounds = $state<Record<string, TesterRelays>>({});
	let error = $state('');
	const open = new SvelteSet<string>();

	async function load() {
		try {
			const [list, relays] = await Promise.all([fetchTesterInstalls(), fetchTesterRelays('24h')]);
			installs = [...list.installs].sort((a, b) => Number(b.online) - Number(a.online) || b.last_seen.localeCompare(a.last_seen));
			rounds = Object.fromEntries(relays.installs.map((r) => [r.install_id, r]));
			error = '';
		} catch (e) {
			error = describeApiError(e);
		}
	}

	// Rename in place: the name is how the owner says who to look at
	// («посмотри у Леши»), and the agent finds the logs by it.
	let editing = $state<string | null>(null);
	let draftAlias = $state('');

	function startRename(i: TesterInstall) {
		editing = i.install_id;
		draftAlias = i.alias;
	}

	async function commitRename(i: TesterInstall) {
		const name = draftAlias.trim();
		editing = null;
		if (!name || name === i.alias) return;
		try {
			await renameTester(i.install_id, name, i.note);
			await load();
		} catch (e) {
			error = describeApiError(e);
		}
	}

	function toggle(id: string) {
		if (open.has(id)) open.delete(id);
		else open.add(id);
	}

	const alive = (id: string) => rounds[id]?.relays.filter((r) => r.up).length ?? null;

	const C = {
		orange: 'oklch(0.78 0.16 65)',
		blue: 'oklch(0.70 0.16 230)',
		red: 'oklch(0.68 0.20 25)',
	};
	const minutes = (v: number) => `${Number.isInteger(v) || v >= 10 ? Math.round(v) : v.toFixed(1)} мин`;
	const ms = (v: number) => `${Math.round(v)} мс`;

	const linesOf = (id: string): TesterLines | null => {
		if (!charts) return null;
		const none = charts.buckets.map(() => null);
		return charts.testers[id] ?? { lost: none, ruMs: none, ruMissed: none };
	};

	/** The latest bucket anything was measured in, with its time. */
	function last(values: (number | null)[] | undefined): { v: number; at: string } | null {
		if (!values || !charts) return null;
		for (let i = values.length - 1; i >= 0; i--) {
			const v = values[i];
			if (v !== null && v !== undefined) return { v, at: charts.buckets[i] };
		}
		return null;
	}

	const bucketTime = (at: string) =>
		new Date(at).toLocaleString('ru', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' });

	/** Minutes nobody in Russia answered, as marks on the network chart. */
	const missedMarks = (l: TesterLines): TrendMark[] =>
		(charts?.buckets ?? []).flatMap((at, i) => {
			const n = l.ruMissed[i];
			return n ? [{ at, label: `${Math.round(n)} мин без ответа`, color: C.red }] : [];
		});

	onMount(load);
</script>

<section class="card">
	{#if error}<p class="bad">{error}</p>{/if}
	{#if installs.length === 0}
		<p class="empty">ни один тестер ещё не выходил на связь</p>
	{:else}
		<table class="rows testers">
			<thead>
				<tr>
					<th>тестер</th>
					<th>клиент</th>
					<th>профили</th>
					<th class="r">простой</th>
					<th class="r">сеть до РФ</th>
					<th class="r">на связи</th>
				</tr>
			</thead>
			<tbody>
				{#each installs as i (i.install_id)}
					{@const l = linesOf(i.install_id)}
					{@const lost = last(l?.lost)}
					{@const ru = last(l?.ruMs)}
					<tr class="row" class:opened={open.has(i.install_id)} onclick={() => toggle(i.install_id)}>
						<td>
							<span class="caret">{open.has(i.install_id) ? '▾' : '▸'}</span>
							{#if editing === i.install_id}
								<input
									class="field rename"
									bind:value={draftAlias}
									onclick={(e) => e.stopPropagation()}
									onblur={() => commitRename(i)}
									onkeydown={(e) => {
										if (e.key === 'Enter') e.currentTarget.blur();
										if (e.key === 'Escape') {
											draftAlias = i.alias;
											editing = null;
										}
									}}
									{@attach (el: HTMLInputElement) => el.select()}
								/>
							{:else}
								<i class="status" class:on={i.online}></i><button
									class="name"
									title="переименовать"
									onclick={(e) => {
										e.stopPropagation();
										startRename(i);
									}}>{i.alias}</button
								>
							{/if}
						</td>
						<td class="mono dim">{i.platform} {i.version}</td>
						<td class="mono">
							{#if alive(i.install_id) === null}-{:else}{alive(i.install_id)} из {rounds[i.install_id].relays.length}{/if}
						</td>
						<td class="r mono" title={lost ? `отрезок с ${bucketTime(lost.at)}` : 'туннель не включали за сутки'}>
							{#if lost}<span class:warn={lost.v > 0}>{minutes(lost.v)}</span>{:else}<span class="dim">-</span>{/if}
						</td>
						<td class="r mono" title={ru ? `медиана за отрезок с ${bucketTime(ru.at)}` : 'нет замеров в окне'}>
							{#if ru}{ms(ru.v)}{:else}<span class="dim">-</span>{/if}
						</td>
						<td class="r mono dim">{ago(new Date(i.last_seen).getTime())}</td>
					</tr>
					{#if open.has(i.install_id)}
						<tr class="charts-row">
							<td colspan="6">
								{#if !charts || !l}
									<p class="dim">загружаю графики…</p>
								{:else}
									<div class="pair">
										<div class="chart-card">
											<h3 class="card-title">Простой</h3>
											<TrendChart
												buckets={charts.buckets}
												bucketSeconds={charts.bucketSeconds}
												formatAxis={minutes}
												height={135}
												series={[{ key: 'lost', label: 'Простой', color: C.orange, primary: true, values: l.lost, format: minutes, missing: 'туннель выключен' }]}
											/>
										</div>
										<div class="chart-card">
											<h3 class="card-title">Сеть до РФ</h3>
											<TrendChart
												buckets={charts.buckets}
												bucketSeconds={charts.bucketSeconds}
												formatAxis={ms}
												height={135}
												marks={missedMarks(l)}
												series={[{ key: 'ru', label: 'Сеть до РФ', color: C.blue, primary: true, values: l.ruMs, format: ms, missing: 'не мерили' }]}
											/>
										</div>
									</div>
								{/if}
							</td>
						</tr>
					{/if}
				{/each}
			</tbody>
		</table>
	{/if}
</section>

<style>
	.testers {
		table-layout: fixed;
	}

	.testers th:nth-child(1) {
		width: 28%;
	}

	.testers th:nth-child(2) {
		width: 20%;
	}

	.testers th:nth-child(3) {
		width: 12%;
	}

	.testers th:nth-child(4),
	.testers th:nth-child(5) {
		width: 14%;
	}

	.testers th:nth-child(6) {
		width: 12%;
	}

	.r {
		text-align: right;
		white-space: nowrap;
	}

	.row {
		cursor: pointer;
	}

	.row:hover td {
		background: oklch(0.21 0 0);
	}

	.caret {
		display: inline-block;
		width: 14px;
		color: var(--text-3);
		font-size: 11px;
	}

	.name {
		padding: 0;
		border: 0;
		background: none;
		color: var(--text);
		font-family: var(--mono);
		font-size: inherit;
		cursor: text;
		text-align: left;
	}

	.name:hover {
		text-decoration: underline dotted;
		text-underline-offset: 3px;
	}

	.status {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		margin-right: 10px;
		background: var(--text-3);
		opacity: 0.5;
	}

	.status.on {
		background: oklch(0.75 0.18 155);
		opacity: 1;
	}

	.rename {
		width: 160px;
	}

	.charts-row td {
		padding-top: 4px;
		padding-bottom: 16px;
	}

	.pair {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 16px;
	}

	.chart-card {
		background: var(--card);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 16px;
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.card-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
		margin: 0 0 8px;
	}

	@media (max-width: 900px) {
		.pair {
			grid-template-columns: 1fr;
		}
	}
</style>
