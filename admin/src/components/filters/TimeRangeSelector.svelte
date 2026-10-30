<script lang="ts">
	import { timeRange, timeRangeLabels, type TimeRange } from '$lib/stores/time-range';

	const options = Object.entries(timeRangeLabels) as [TimeRange, string][];
</script>

<div class="time-range">
	{#each options as [value, label]}
		<button class="option" class:active={$timeRange === value} onclick={() => timeRange.set(value)}>
			{label}
		</button>
	{/each}
</div>

<style>
	.time-range {
		display: flex;
		gap: 2px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
		padding: 2px;
	}

	.option {
		font-size: 12px;
		font-weight: 500;
		color: var(--text-secondary);
		padding: 5px 12px;
		border-radius: 4px;
		transition: all var(--transition-fast);
		white-space: nowrap;
	}

	.option:hover {
		color: var(--text-primary);
	}

	.option.active {
		color: var(--text-primary);
		background: var(--surface-active);
	}
</style>
