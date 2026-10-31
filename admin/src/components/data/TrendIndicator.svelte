<script lang="ts">
	import { icons } from '$components/icons';

	interface Props {
		value: number;
		direction: 'up' | 'down' | 'neutral';
		suffix?: string;
	}

	let { value, direction, suffix = '' }: Props = $props();
</script>

<span class="trend {direction}">
	{#if direction === 'up'}
		{@html icons.arrowUp}
	{:else if direction === 'down'}
		{@html icons.arrowDown}
	{/if}
	<span class="value mono">{value.toFixed(1)}%</span>
	{#if suffix}
		<span class="suffix">{suffix}</span>
	{/if}
</span>

<style>
	.trend {
		display: inline-flex;
		align-items: center;
		gap: 3px;
		font-size: 12px;
		font-weight: 500;
	}

	.trend.up {
		color: var(--trend-up);
	}

	.trend.down {
		color: var(--trend-down);
	}

	.trend.neutral {
		color: var(--trend-neutral);
	}

	.suffix {
		color: var(--text-muted);
		font-weight: 400;
	}
</style>
