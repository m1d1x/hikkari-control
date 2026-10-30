<script lang="ts">
	import type { AdminLogCats } from '$lib/api/logs';

	interface Props {
		cats: AdminLogCats;
		levels: string[];
		source: string;
		catSel: string[];
		userId: string;
		q: string;
		from: string;
		to: string;
		onChange: () => void;
	}

	let {
		cats,
		levels = $bindable(),
		source = $bindable(),
		catSel = $bindable(),
		userId = $bindable(),
		q = $bindable(),
		from = $bindable(),
		to = $bindable(),
		onChange,
	}: Props = $props();

	let qInput = $state(q);
	let debounceTimer: ReturnType<typeof setTimeout> | null = null;

	function onQInput() {
		if (debounceTimer) clearTimeout(debounceTimer);
		debounceTimer = setTimeout(() => {
			q = qInput;
			onChange();
		}, 300);
	}

	function toggleLevel(lv: string) {
		if (levels.includes(lv)) {
			levels = levels.filter((x) => x !== lv);
		} else {
			levels = [...levels, lv];
		}
		onChange();
	}

	function toggleCat(c: string) {
		if (catSel.includes(c)) {
			catSel = catSel.filter((x) => x !== c);
		} else {
			catSel = [...catSel, c];
		}
		onChange();
	}

	function onSourceChange(e: Event) {
		source = (e.currentTarget as HTMLSelectElement).value;
		onChange();
	}

	function onUserIdChange() {
		onChange();
	}

	function onDateChange() {
		onChange();
	}

	function resetFilters() {
		levels = [];
		source = '';
		catSel = [];
		userId = '';
		q = '';
		qInput = '';
		from = '';
		to = '';
		onChange();
	}

	/** Deterministic HSL hue from category name for stable per-cat color. */
	function catColor(name: string): string {
		let h = 0;
		for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
		const hue = h % 360;
		return `oklch(0.70 0.12 ${hue})`;
	}
</script>

<div class="filters">
	<div class="row">
		<div class="group">
			<span class="label">Level</span>
			<div class="chips">
				{#each cats.levels as lv}
					<button
						class="chip chip-{lv}"
						class:active={levels.includes(lv)}
						onclick={() => toggleLevel(lv)}
						type="button"
					>
						{lv}
					</button>
				{/each}
			</div>
		</div>

		<div class="group">
			<span class="label">Source</span>
			<select class="input" value={source} onchange={onSourceChange}>
				<option value="">All</option>
				{#each cats.sources as s}
					<option value={s}>{s}</option>
				{/each}
			</select>
		</div>

		<div class="group grow">
			<span class="label">Search</span>
			<input
				class="input"
				type="text"
				placeholder="substring in msg..."
				bind:value={qInput}
				oninput={onQInput}
			/>
		</div>
	</div>

	<div class="row">
		<div class="group">
			<span class="label">Categories</span>
			<div class="chips">
				{#if cats.cats.length === 0}
					<span class="muted">нет категорий за 7 дней</span>
				{/if}
				{#each cats.cats as c}
					<button
						class="chip cat-chip"
						class:active={catSel.includes(c)}
						style="--chip-color: {catColor(c)};"
						onclick={() => toggleCat(c)}
						type="button"
					>
						{c}
					</button>
				{/each}
			</div>
		</div>
	</div>

	<div class="row">
		<div class="group">
			<span class="label">User ID</span>
			<input
				class="input mono"
				type="text"
				placeholder="UUID"
				bind:value={userId}
				onchange={onUserIdChange}
			/>
		</div>
		<div class="group">
			<span class="label">From</span>
			<input class="input" type="datetime-local" bind:value={from} onchange={onDateChange} />
		</div>
		<div class="group">
			<span class="label">To</span>
			<input class="input" type="datetime-local" bind:value={to} onchange={onDateChange} />
		</div>
		<div class="group">
			<button class="reset-btn" type="button" onclick={resetFilters}>Reset</button>
		</div>
	</div>
</div>

<style>
	.filters {
		display: flex;
		flex-direction: column;
		gap: 10px;
		background: var(--card-bg);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		padding: 12px;
		margin-bottom: 12px;
	}
	.row {
		display: flex;
		gap: 16px;
		flex-wrap: wrap;
		align-items: flex-end;
	}
	.group {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.group.grow {
		flex: 1;
		min-width: 200px;
	}
	.label {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}
	.chips {
		display: flex;
		gap: 4px;
		flex-wrap: wrap;
	}

	/* Base chip — no color override here; level/cat subclasses handle it */
	.chip {
		padding: 4px 10px;
		border-radius: 12px;
		font-size: 11px;
		font-family: inherit;
		cursor: pointer;
		border: 1px solid var(--border-subtle);
		background: transparent;
		color: var(--text-secondary);
		text-transform: uppercase;
		transition: all var(--transition-fast);
	}
	.chip:hover {
		background: var(--surface-hover);
	}

	/* ── Level chip default (inactive) colours ── */
	.chip-error {
		color: var(--status-critical);
		border-color: color-mix(in oklab, var(--status-critical) 40%, transparent);
		background: color-mix(in oklab, var(--status-critical) 12%, transparent);
	}
	.chip-warn {
		color: var(--status-warning);
		border-color: color-mix(in oklab, var(--status-warning) 40%, transparent);
		background: color-mix(in oklab, var(--status-warning) 12%, transparent);
	}
	.chip-info {
		color: var(--chart-2);
		border-color: color-mix(in oklab, var(--chart-2) 40%, transparent);
		background: color-mix(in oklab, var(--chart-2) 12%, transparent);
	}
	.chip-debug {
		color: var(--text-secondary);
		border-color: color-mix(in oklab, var(--text-secondary) 40%, transparent);
		background: color-mix(in oklab, var(--text-secondary) 12%, transparent);
	}
	.chip-trace {
		color: var(--text-muted);
		border-color: color-mix(in oklab, var(--text-muted) 40%, transparent);
		background: color-mix(in oklab, var(--text-muted) 12%, transparent);
	}

	/* ── Active state: solid filled background with the chip colour ── */
	.chip.active {
		font-weight: 600;
	}
	.chip-error.active {
		background: var(--status-critical);
		border-color: var(--status-critical);
		color: #fff;
	}
	.chip-warn.active {
		background: var(--status-warning);
		border-color: var(--status-warning);
		color: #0a0a0a;
	}
	.chip-info.active {
		background: var(--chart-2);
		border-color: var(--chart-2);
		color: #0a0a0a;
	}
	.chip-debug.active,
	.chip-trace.active {
		background: var(--text-secondary);
		border-color: var(--text-secondary);
		color: var(--bg);
	}

	/* ── Category chips — color driven by --chip-color CSS var ── */
	.cat-chip {
		color: var(--chip-color);
		border-color: color-mix(in oklab, var(--chip-color) 40%, transparent);
		background: color-mix(in oklab, var(--chip-color) 12%, transparent);
	}
	.cat-chip.active {
		background: var(--chip-color);
		border-color: var(--chip-color);
		color: #0a0a0a;
		font-weight: 600;
	}

	/* ── Inputs ── */
	.input {
		/* color-scheme: dark tells Chromium/Electron to render native widgets
		   (dropdown list, calendar popup, scrollbars) with the OS dark theme —
		   pure CSS on <option> is ignored on Windows otherwise. */
		color-scheme: dark;
		height: 30px;
		padding: 0 10px;
		border-radius: 6px;
		background: var(--bg);
		border: 1px solid var(--border-subtle);
		color: var(--text-primary);
		font-size: 12px;
		font-family: inherit;
	}
	.input.mono {
		font-family: var(--font-mono);
	}
	.input option {
		background: var(--card-bg);
		color: var(--text-primary);
	}

	.reset-btn {
		height: 30px;
		padding: 0 14px;
		border-radius: 6px;
		font-size: 12px;
		font-family: inherit;
		cursor: pointer;
		border: 1px solid var(--border-subtle);
		background: transparent;
		color: var(--text-secondary);
	}
	.reset-btn:hover {
		background: var(--surface-hover);
		color: var(--text-primary);
	}
	.muted {
		font-size: 11px;
		color: var(--text-muted);
	}
</style>
