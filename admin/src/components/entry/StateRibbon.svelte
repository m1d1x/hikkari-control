<script lang="ts">
	import { tick } from 'svelte';
	import { bucketLabel, bucketTitle } from '$lib/api/entry';
	import type { TrendSeries } from './TrendChart.svelte';

	// A state over time, not a value: which egress carried the receiver, which
	// border point held the session. Coloured cells in a row, a seam is a
	// switch, the axis under it says when. Hovering gives the moment at once:
	// a crosshair across every row, who is where, and «aeznl1, с 10:00 до 12:30»
	// for the run under the cursor. A click pins it, Esc drops it.

	let {
		buckets,
		bucketSeconds,
		series,
		mode = 'carrier',
		empty = 'нет замеров за это окно',
	}: {
		buckets: string[];
		bucketSeconds: number;
		series: TrendSeries[];
		/** carrier: one row, the holder of the bucket; rows: one row per series, a live cell is a bucket it held */
		mode?: 'carrier' | 'rows';
		empty?: string;
	} = $props();

	const end = (iso: string) => new Date(Date.parse(iso) + bucketSeconds * 1000).toISOString();
	const span = $derived(bucketSeconds);
	const n = $derived(buckets.length);

	let plotEl: HTMLDivElement | undefined = $state();
	let tipEl: HTMLDivElement | undefined = $state();
	let hi = $state(-1);
	let pinned = $state(false);
	let guideLeft = $state(0);
	let guideTop = $state(0);
	let guideH = $state(0);
	let tipX = $state(0);
	let tipY = $state(0);

	// A window switch changes the bucket count: the old hover means nothing now.
	let dataKey = '';
	$effect(() => {
		const key = `${buckets.length}|${series.length}`;
		if (key === dataKey) return;
		dataKey = key;
		hi = -1;
		pinned = false;
	});

	const picked = $derived(
		buckets.map((at, i) => {
			let best: TrendSeries | null = null;
			let held = 0;
			for (const s of series) {
				const v = s.values[i];
				if (v !== null && v !== undefined && v > held) {
					held = v;
					best = s;
				}
			}
			return { at, i, held, label: best?.label ?? '', color: best?.color ?? null };
		}),
	);

	const bands = $derived(picked);

	const runs = $derived.by(() => {
		const out: { label: string; from: number; to: number }[] = [];
		for (const b of bands) {
			const last = out[out.length - 1];
			if (last && last.label === b.label) last.to += 1;
			else out.push({ label: b.label, from: out.length ? last.to : 0, to: (out.length ? last.to : 0) + 1 });
		}
		return out.filter((r) => r.label !== '' && (r.to - r.from) / bands.length >= 0.12);
	});

	// dates once the strip is longer than a couple of days
	const labelStep = $derived(buckets.length * span >= 2 * 86_400 ? Math.max(span, 86_400) : span);

	const ticks = $derived(
		[0, 0.25, 0.5, 0.75, 1].map((f) => {
			const i = Math.min(buckets.length - 1, Math.round(f * (buckets.length - 1)));
			return bucketLabel(buckets[i], labelStep);
		}),
	);

	const hasData = $derived(
		mode === 'carrier' ? picked.some((b) => b.label !== '') : series.some((s) => s.values.some((v) => v !== null && v > 0)),
	);

	// a bucket that no longer exists after the window changed is no hover at all
	const at = $derived(hi >= 0 && hi < n ? hi : -1);

	const stateOf = (v: number | null | undefined) => (v === null || v === undefined ? 'na' : v ? 'up' : 'down');

	function runStart(values: (number | null)[], i: number, cat: (v: number | null | undefined) => string): number {
		let a = i;
		while (a > 0 && cat(values[a - 1]) === cat(values[i])) a--;
		return a;
	}

	function runEnd(values: (number | null)[], i: number, cat: (v: number | null | undefined) => string): number {
		let b = i;
		while (b + 1 < values.length && cat(values[b + 1]) === cat(values[i])) b++;
		return b;
	}

	/** The hours a run covers; the dates are spelled out when it crosses midnight. */
	function spanText(a: number, b: number): string {
		const from = buckets[a];
		const to = end(buckets[b]);
		const sameDay = new Date(from).toDateString() === new Date(to).toDateString();
		return sameDay
			? `${bucketLabel(from, span)}–${bucketLabel(to, span)}`
			: `${bucketLabel(from, 86_400)} ${bucketLabel(from, span)} – ${bucketLabel(to, 86_400)} ${bucketLabel(to, span)}`;
	}

	interface TipRow {
		key: string;
		label: string;
		color: string;
		state: string;
		bad: boolean;
		when: string;
	}

	const tipTime = $derived(
		at < 0
			? ''
			: mode === 'carrier'
				? `${bucketTitle(buckets[at], span)} · ${
						picked[at].held > 0
							? `${Math.round(picked[at].held)} из ${Math.round(span / 60)} мин`
							: series.every((s) => s.values[at] === null || s.values[at] === undefined)
								? 'нет замеров'
								: 'никто не вёз'
					}`
				: bucketTitle(buckets[at], span),
	);

	const tipRows = $derived.by<TipRow[]>(() => {
		if (at < 0) return [];
		if (mode === 'carrier') {
			return series.map((s) => {
				const carried = (s.values[at] ?? 0) > 0;
				if (!carried) return { key: s.key, label: s.label, color: s.color, state: 'не вёз', bad: false, when: '' };
				const a = runStart(s.values, at, (v) => (v !== null && v !== undefined && v > 0 ? 'up' : 'down'));
				const b = runEnd(s.values, at, (v) => (v !== null && v !== undefined && v > 0 ? 'up' : 'down'));
				return { key: s.key, label: s.label, color: s.color, state: 'вёз', bad: false, when: spanText(a, b) };
			});
		}
		return series.map((s) => {
			const v = s.values[at] ?? null;
			const cat = stateOf(v);
			const a = runStart(s.values, at, stateOf);
			return {
				key: s.key,
				label: s.label,
				color: s.color,
				state: cat === 'na' ? 'нет замеров' : cat === 'up' ? 'держит' : 'молчит',
				bad: v !== null && v !== undefined && !v,
				when: cat === 'na' ? '' : `с ${bucketLabel(buckets[a], span)}`,
			};
		});
	});

	// ── what the pointer is over ─────────────────────────────────────────────

	interface Geometry {
		plotLeft: number;
		plotTop: number;
		plotWidth: number;
		plotHeight: number;
		barLeft: number;
		barWidth: number;
		top: number;
		bottom: number;
	}

	function geometry(): Geometry | null {
		if (!plotEl) return null;
		const ribbons = plotEl.querySelectorAll<HTMLElement>('.ribbon');
		if (!ribbons.length) return null;
		const first = ribbons[0];
		const last = ribbons[ribbons.length - 1];
		const rect = plotEl.getBoundingClientRect();
		return {
			plotLeft: rect.left,
			plotTop: rect.top,
			plotWidth: rect.width,
			plotHeight: rect.height,
			barLeft: first.getBoundingClientRect().left - rect.left,
			barWidth: first.getBoundingClientRect().width,
			top: first.offsetTop,
			bottom: last.offsetTop + last.offsetHeight,
		};
	}

	function indexAt(clientX: number, g: Geometry): number {
		const x = clientX - g.plotLeft - g.barLeft;
		if (x < 0 || x > g.barWidth) return -1;
		return Math.max(0, Math.min(n - 1, Math.floor((x / g.barWidth) * n)));
	}

	function setGuide(i: number, g: Geometry) {
		guideLeft = g.barLeft + ((i + 0.5) * g.barWidth) / n;
		guideTop = g.top - 2;
		guideH = g.bottom - g.top + 4;
	}

	function place(clientX: number, clientY: number, g: Geometry) {
		const tw = tipEl?.offsetWidth ?? 0;
		const th = tipEl?.offsetHeight ?? 0;
		let x = clientX - g.plotLeft + 14;
		if (x + tw > g.plotWidth) x = Math.max(0, clientX - g.plotLeft - tw - 14);
		let y = clientY - g.plotTop + 16;
		if (y + th > g.plotHeight) y = Math.max(0, g.plotHeight - th);
		tipX = x;
		tipY = y;
	}

	function show(i: number, clientX: number, clientY: number, g: Geometry) {
		hi = i;
		setGuide(i, g);
		// the tooltip's own width follows its new text, so place it after that renders
		void tick().then(() => place(clientX, clientY, g));
	}

	function clear() {
		if (pinned) return;
		hi = -1;
	}

	function move(e: PointerEvent) {
		if (pinned) return;
		const g = geometry();
		if (!g) return;
		const i = indexAt(e.clientX, g);
		if (i < 0) return clear();
		if (i !== at) show(i, e.clientX, e.clientY, g);
		else place(e.clientX, e.clientY, g);
	}

	function down(e: PointerEvent) {
		const g = geometry();
		if (!g) return;
		const i = indexAt(e.clientX, g);
		if (i < 0) return;
		if (pinned && i === at) {
			pinned = false;
			hi = -1;
			return;
		}
		pinned = true;
		show(i, e.clientX, e.clientY, g);
	}

	function key(e: KeyboardEvent) {
		if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
		e.preventDefault();
		const start = at < 0 ? n - 1 : at;
		const i = Math.max(0, Math.min(n - 1, start + (e.key === 'ArrowLeft' ? -1 : 1)));
		const g = geometry();
		if (!plotEl || !g) return;
		pinned = true;
		const rect = plotEl.getBoundingClientRect();
		show(i, rect.left + g.barLeft + ((i + 0.5) * g.barWidth) / n, rect.top + g.top + 10, g);
	}

	function esc(e: KeyboardEvent) {
		if (e.key !== 'Escape' || !pinned) return;
		pinned = false;
		hi = -1;
	}

	function outside(e: PointerEvent) {
		if (!pinned || plotEl?.contains(e.target as Node)) return;
		pinned = false;
		hi = -1;
	}
</script>

<svelte:window onkeydown={esc} onpointerdown={outside} />

{#if !hasData}
	<p class="empty">{empty}</p>
{:else}
	<div
		class="plot"
		bind:this={plotEl}
		role="slider"
		tabindex="0"
		aria-orientation="horizontal"
		aria-label={mode === 'carrier' ? 'кто вёз, по отрезкам' : 'точки, по отрезкам'}
		aria-valuemin={0}
		aria-valuemax={Math.max(0, n - 1)}
		aria-valuenow={at < 0 ? 0 : at}
		aria-valuetext={at < 0 ? '' : tipTime}
		onpointermove={move}
		onpointerleave={clear}
		onpointerdown={down}
		onkeydown={key}
	>
		{#if mode === 'carrier'}
			<div class="ribbon" role="img" aria-label="кто вёз, по отрезкам">
				{#each bands as band (band.at)}
					<span
						class="cell"
						class:void={!band.label}
						class:hot={at === band.i}
						style={band.color ? `background: ${band.color}` : ''}
					></span>
				{/each}
				{#each runs as run (run.from)}
					<span
						class="tag"
						class:on={at >= run.from && at < run.to}
						style="left: {(run.from / bands.length) * 100}%; width: {((run.to - run.from) / bands.length) * 100}%;"
						>{run.label}</span
					>
				{/each}
			</div>
		{:else}
			{#each series as s (s.key)}
				<div class="row">
					<span class="who">{s.label}</span>
					<div class="ribbon" role="img" aria-label={s.label}>
						{#each s.values as v, i (i)}
							<span
								class="cell"
								class:hot={at === i}
								style={v === null || v === undefined
									? 'background: oklch(0.2 0 0)'
									: v
										? 'background: oklch(0.27 0 0)'
										: 'background: #e66767'}
							></span>
						{/each}
					</div>
				</div>
			{/each}
		{/if}
		<div class="axis" class:offset={mode === 'rows'}>
			{#each ticks as t, i (i)}<span>{t}</span>{/each}
		</div>
		<span class="guide" class:on={at >= 0} style="left: {guideLeft}px; top: {guideTop}px; height: {guideH}px"></span>
		{#if at >= 0}
			<div class="tip" class:pinned bind:this={tipEl} style="left: {tipX}px; top: {tipY}px">
				<div class="tip-time">{tipTime}</div>
				{#each tipRows as r (r.key)}
					<div class="tip-row">
						<span class="dot" style="background: {r.color}"></span>
						<span class="tip-name">{r.label}</span>
						<span class="tip-state" class:bad={r.bad}>{r.state}</span>
						{#if r.when}<span class="tip-when">{r.when}</span>{/if}
					</div>
				{/each}
			</div>
		{/if}
	</div>
{/if}

<style>
	.plot {
		position: relative;
		outline: none;
		user-select: none;
	}

	.plot:focus-visible {
		box-shadow: 0 0 0 1px var(--border);
		border-radius: 4px;
	}

	.ribbon {
		position: relative;
		display: flex;
		width: 100%;
		height: 26px;
		border-radius: 3px;
		overflow: hidden;
		cursor: crosshair;
	}

	.tag {
		position: absolute;
		top: 50%;
		transform: translateY(-50%);
		padding: 1px 6px;
		border-radius: 3px;
		background: rgba(0, 0, 0, 0.45);
		color: #fff;
		font-size: 10px;
		line-height: 14px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		pointer-events: none;
		transition: background-color 70ms linear;
	}

	.tag.on {
		background: rgba(0, 0, 0, 0.72);
	}

	.cell {
		flex: 1 1 0;
		min-width: 1px;
		transition: box-shadow 70ms linear;
	}

	.cell.void {
		background: oklch(0.24 0 0);
	}

	.cell.hot {
		box-shadow: inset 0 0 0 1px rgba(255, 255, 255, 0.85);
	}

	.guide {
		position: absolute;
		width: 1px;
		background: rgba(255, 255, 255, 0.45);
		pointer-events: none;
		opacity: 0;
		transition: opacity 80ms linear;
	}

	.guide.on {
		opacity: 1;
	}

	.row {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.row + .row {
		margin-top: 3px;
	}

	.who {
		flex: 0 0 64px;
		font-size: 11px;
		color: var(--text-2);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.row .ribbon {
		flex: 1;
	}

	.axis.offset {
		padding-left: 72px;
	}

	.axis {
		display: flex;
		justify-content: space-between;
		margin-top: 4px;
		color: var(--text-2);
		font-family: var(--mono, 'IBM Plex Mono', monospace);
		font-size: 11px;
		font-variant-numeric: tabular-nums;
	}

	.tip {
		position: absolute;
		z-index: 5;
		min-width: 168px;
		max-width: 320px;
		padding: 8px 10px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: oklch(0.17 0 0);
		box-shadow: 0 8px 24px rgba(0, 0, 0, 0.55);
		pointer-events: none;
	}

	.tip.pinned {
		border-color: var(--text-2);
	}

	.tip-time {
		margin-bottom: 5px;
		color: var(--text-2);
		font-family: var(--mono, 'IBM Plex Mono', monospace);
		font-size: 10px;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}

	.tip-row {
		display: flex;
		align-items: baseline;
		gap: 6px;
		font-size: 12px;
		white-space: nowrap;
	}

	.tip-row + .tip-row {
		margin-top: 3px;
	}

	.dot {
		flex: 0 0 6px;
		width: 6px;
		height: 6px;
		border-radius: 50%;
		transform: translateY(-1px);
	}

	.tip-name {
		font-weight: 600;
	}

	.tip-state {
		color: var(--text-2);
	}

	.tip-state.bad {
		color: #e66767;
	}

	.tip-when {
		margin-left: auto;
		padding-left: 10px;
		color: var(--text-2);
		font-family: var(--mono, 'IBM Plex Mono', monospace);
		font-size: 10px;
		font-variant-numeric: tabular-nums;
	}

	.empty {
		margin: 0;
		color: var(--text-2);
		font-size: 12px;
	}
</style>
