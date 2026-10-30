<script lang="ts" module>
	export interface ChipOption {
		value: string;
		label: string;
	}

	export interface ChipGroup {
		/** Identifies the group in the onChange callback. */
		key: string;
		options: ChipOption[];
		value: string;
	}
</script>

<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		groups: ChipGroup[];
		onChange: (groupKey: string, value: string) => void;
		onRefresh?: () => void;
		loading?: boolean;
		/** Anything that is not a chip — a checkbox, a counter. */
		extra?: Snippet;
	}

	let { groups, onChange, onRefresh, loading = false, extra }: Props = $props();
</script>

<!--
	One filter bar for every list on this page. A select hides its options until
	clicked and says nothing about what else exists; chips show the whole set and
	the current choice at once, which is why the ideas list was the one people
	liked using.
-->
<div class="chip-bar">
	{#each groups as group (group.key)}
		<div class="chip-group" role="group">
			{#each group.options as opt (opt.value)}
				<button
					type="button"
					class="chip"
					class:active={group.value === opt.value}
					aria-pressed={group.value === opt.value}
					onclick={() => onChange(group.key, opt.value)}
				>
					{opt.label}
				</button>
			{/each}
		</div>
	{/each}

	{#if extra}
		<div class="extra">{@render extra()}</div>
	{/if}

	{#if onRefresh}
		<button type="button" class="chip refresh" onclick={onRefresh} disabled={loading}>
			{loading ? '…' : 'обновить'}
		</button>
	{/if}
</div>

<style>
	.chip-bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px 14px;
		margin-bottom: 16px;
	}

	.chip-group {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	/* Groups stay visually separate without a divider element. */
	.chip-group + .chip-group {
		padding-left: 14px;
		border-left: 1px solid var(--border-subtle);
	}

	.chip {
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-raised);
		color: var(--text-secondary);
		cursor: pointer;
		font: inherit;
		font-size: 12px;
		padding: 6px 10px;
		transition:
			border-color var(--transition-fast),
			color var(--transition-fast),
			background var(--transition-fast);
	}

	.chip:hover:not(:disabled) {
		color: var(--text-primary);
		background: var(--surface-hover);
	}

	.chip.active {
		border-color: var(--primary);
		color: var(--text-primary);
		background: var(--primary-subtle);
	}

	.chip:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.extra {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		color: var(--text-secondary);
	}

	.refresh {
		margin-left: auto;
	}
</style>
