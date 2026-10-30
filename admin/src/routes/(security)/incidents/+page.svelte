<script lang="ts">
	import { onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import Panel from '$components/entry/ui/Panel.svelte';
	import MetricHero from '$components/entry/ui/MetricHero.svelte';
	import RangeChips from '$components/entry/ui/RangeChips.svelte';
	import TimeSeries from '$components/entry/ui/TimeSeries.svelte';
	import IncidentQueue from '$components/entry/IncidentQueue.svelte';
	import IncidentTimeline from '$components/entry/IncidentTimeline.svelte';
	import type { HeroRow } from '$components/entry/ui/MetricHero.svelte';
	import { CHART } from '$components/entry/ui/colors';
	import {
		describeEntryError,
		fetchIncidents,
		type EntryIncidents,
		type IncidentFeedRow,
		type OverviewWindow,
	} from '$lib/api/entry';

	// Incidents (roadmap 42 К2): every "something is wrong with a machine" as
	// one record, from the first failure to the closing.
	//
	// The screen answers two questions and nothing else: how much of this the
	// hub handled alone, and how long the handling took. Both are drawn over
	// time — a single current figure for either only means something to
	// someone who memorised yesterday's.

	const WINDOWS: readonly { key: OverviewWindow; label: string }[] = [
		{ key: '24h', label: '24 ч' },
		{ key: '7d', label: '7 дн' },
		{ key: '30d', label: '30 дн' },
	];

	let data = $state<EntryIncidents | null>(null);
	let loadError = $state<string | null>(null);
	let window = $state<OverviewWindow>('7d');

	const missingOf = $derived(new Map((data?.missing ?? []).map((m) => [m.figure, m.why])));

	async function load() {
		try {
			data = await fetchIncidents(window);
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
		return WINDOWS.find((x) => x.key === w)?.label ?? w;
	}

	onMount(load);

	// ---------- 1. how much the hub closed alone ----------

	// Only the ways an incident *ended* belong under a share of endings. The
	// ones still open are said in the panel's head instead, where they are a
	// backlog rather than an outcome.
	const automationRows = $derived.by<HeroRow[]>(() => {
		const by = data?.automation.by;
		if (!by) return [];
		return [
			{ label: 'закрыл хаб', value: `${by.auto}`, color: CHART.ok },
			{ label: 'потребовал оператора', value: `${by.handed}`, color: CHART.warn },
			{ label: 'ремонт не удался', value: `${by.failed}`, color: CHART.bad, tone: 'bad' as const },
			{ label: 'отклонено', value: `${by.dismissed}`, color: CHART.dim },
		];
	});

	// ---------- 2. how long the handling took ----------

	function clock(sec: number | null | undefined): string | null {
		if (sec === null || sec === undefined) return null;
		if (sec < 60) return `${Math.round(sec)} с`;
		if (sec < 3600) return `${Math.round(sec / 60)} мин`;
		return `${(sec / 3600).toFixed(1)} ч`;
	}

	const stageRows = $derived.by<HeroRow[]>(() => {
		const s = data?.stages;
		if (!s) return [];
		return [
			{ label: 'до обнаружения', value: clock(s.toNoticeSec) ?? 'нет данных', color: CHART.info },
			{ label: 'до начала ремонта', value: clock(s.toDecideSec) ?? 'нет данных', color: CHART.warn },
			{ label: 'до допуска замены', value: clock(s.toRepairSec) ?? 'нет данных', color: CHART.ok },
			{
				label: 'инцидентов в расчёте',
				value: `${s.repairSamples}`,
				separated: true,
				tone: 'dim' as const,
			},
		];
	});

	function median(values: number[]): number | null {
		if (values.length === 0) return null;
		const s = [...values].sort((a, b) => a - b);
		const mid = Math.floor(s.length / 2);
		return s.length % 2 ? s[mid] : (s[mid - 1] + s[mid]) / 2;
	}

	const months = ['янв', 'фев', 'мар', 'апр', 'май', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'];

	function dayLabel(iso: string): string {
		const p = iso.split('-');
		return p.length === 3 ? `${parseInt(p[2], 10)} ${months[parseInt(p[1], 10) - 1] ?? p[1]}` : iso;
	}

	/**
	 * The two clocks per day, in minutes, taken from the incidents' own
	 * timestamps. The hub hands out the window's medians but no series for
	 * them, and these two spans are already on every row of the feed — so the
	 * line is a reading of the same records, not a second measurement. A day
	 * where nothing was repaired stays a gap: the chart does not draw a zero
	 * the records never said.
	 */
	const stageDays = $derived.by(() => {
		const buckets = new Map<string, { decide: number[]; repair: number[] }>();
		for (const d of data?.automation.days ?? []) buckets.set(d.day, { decide: [], repair: [] });

		const put = (row: IncidentFeedRow, key: 'decide' | 'repair', from: string, to: string) => {
			const b = buckets.get(row.openedAt.slice(0, 10));
			if (!b) return;
			const mins = (new Date(to).getTime() - new Date(from).getTime()) / 60_000;
			if (mins >= 0) b[key].push(mins);
		};

		for (const row of data?.feed ?? []) {
			if (row.actedAt) put(row, 'decide', row.openedAt, row.actedAt);
			if (row.actedAt && row.admittedAt) put(row, 'repair', row.actedAt, row.admittedAt);
		}

		const days = [...buckets.keys()].sort();
		return {
			labels: days.map(dayLabel),
			decide: days.map((d) => median(buckets.get(d)!.decide)),
			repair: days.map((d) => median(buckets.get(d)!.repair)),
		};
	});

	/** The window's incidents by the day they ended, stacked by how. */
	const automationDays = $derived.by(() => {
		const days = data?.automation.days ?? [];
		return {
			labels: days.map((d) => dayLabel(d.day)),
			auto: days.map((d) => d.auto),
			handed: days.map((d) => d.handed),
			failed: days.map((d) => d.failed),
			dismissed: days.map((d) => d.dismissed),
		};
	});
</script>

<PageShell>
	<div class="entry-scope incidents-page">
		<div class="head">
			<h1>инциденты</h1>
			<div class="right">
				<RangeChips options={WINDOWS} value={window} onpick={pick} />
			</div>
		</div>

		{#if loadError}
			<p class="err-banner" role="alert">{loadError}</p>
		{:else if !data}
			<p class="hint">загружаю</p>
		{:else}
			<!-- ======== 1. what the hub closed by itself ======== -->
			<Panel>
				<MetricHero
					value={data.automation.autoPct === null ? null : data.automation.autoPct.toFixed(0)}
					unit="%"
					what="закрыто автоматикой"
					missing="за окно не закрылось ни одного инцидента"
					rows={automationRows}
				>
					{#snippet chart()}
						<TimeSeries
							labels={automationDays.labels}
							series={[
								{ name: 'закрыл хаб', color: CHART.ok, type: 'bar', stack: 'how', vals: automationDays.auto },
								{
									name: 'оператор',
									color: CHART.warn,
									type: 'bar',
									stack: 'how',
									vals: automationDays.handed,
								},
								{ name: 'ошибка', color: CHART.bad, type: 'bar', stack: 'how', vals: automationDays.failed },
								{
									name: 'отклонено',
									color: CHART.dim,
									type: 'bar',
									stack: 'how',
									vals: automationDays.dismissed,
								},
							]}
							height="190px"
							emptyNote="за окно не закрылось ни одного инцидента"
						/>
					{/snippet}
				</MetricHero>
			</Panel>

			<!-- ======== 2. how long a repair takes ======== -->
			<Panel>
				<MetricHero
					value={clock(data.stages.toRepairSec)}
					what="медиана ремонта"
					missing={missingOf.get('stages.toRepairSec') ?? 'за окно не допущено ни одной замены'}
					rows={stageRows}
				>
					{#snippet chart()}
						<TimeSeries
							labels={stageDays.labels}
							series={[
								{ name: 'до начала ремонта', color: CHART.warn, vals: stageDays.decide, unit: ' мин' },
								{ name: 'до допуска замены', color: CHART.ok, vals: stageDays.repair, unit: ' мин' },
							]}
							height="190px"
							emptyNote="за окно ни один ремонт не дошёл до допуска"
						/>
					{/snippet}
				</MetricHero>
				{#if missingOf.get('stages.toNoticeSec')}
					<p class="note">до обнаружения — нет данных: {missingOf.get('stages.toNoticeSec')}</p>
				{/if}
			</Panel>

			<!-- ======== 3. the ones waiting for a person ======== -->
			<IncidentQueue rows={data.needsYou} pauses={data.pauses} playbooks={data.playbooks} onchange={load} />

			<!-- ======== 4. everything that happened, with its timeline ======== -->
			<IncidentTimeline rows={data.feed} window={windowLabel(data.window)} />
		{/if}
	</div>
</PageShell>

<style>
	.note {
		margin: 12px 0 0;
		padding-top: 10px;
		border-top: 1px solid var(--border-subtle);
		font-size: 11px;
		color: var(--text-3);
	}
</style>
