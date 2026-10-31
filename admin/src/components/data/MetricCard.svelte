<script lang="ts">
	import TrendIndicator from './TrendIndicator.svelte';
	import SparkLine from '$components/charts/SparkLine.svelte';
	import { formatNumber, formatPercent, formatDuration, formatBytes } from '$lib/utils/format';

	interface Props {
		label: string;
		value: number;
		format?: 'number' | 'percent' | 'duration' | 'bytes';
		trend?: { value: number; direction: 'up' | 'down' | 'neutral' };
		trendLabel?: string;
		sparkData?: number[];
		hint?: string;
		loading?: boolean;
	}

	let { label, value, format = 'number', trend, trendLabel, sparkData, hint, loading = false }: Props = $props();

	function formatValue(v: number, fmt: string): string {
		switch (fmt) {
			case 'percent':
				return formatPercent(v);
			case 'duration':
				return formatDuration(v);
			case 'bytes':
				return formatBytes(v);
			default:
				return formatNumber(v);
		}
	}
</script>

<div class="metric-card" class:loading>
	{#if loading}
		<div class="skeleton-label"></div>
		<div class="skeleton-value"></div>
	{:else}
		<div class="top">
			<span class="label">{label}</span>
			{#if trend}
				<TrendIndicator value={trend.value} direction={trend.direction} suffix={trendLabel} />
			{/if}
		</div>
		<span class="value mono">{formatValue(value, format)}</span>
		{#if hint}
			<span class="hint">{hint}</span>
		{/if}
		{#if sparkData}
			<div class="spark">
				<SparkLine data={sparkData} height="28px" width="100%" />
			</div>
		{/if}
	{/if}
</div>

<style>
	.metric-card {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		padding: var(--card-padding);
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}

	.top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
	}

	.label {
		font-size: 12px;
		font-weight: 500;
		color: var(--text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.value {
		font-size: 28px;
		font-weight: 600;
		color: var(--text-primary);
		line-height: 1.2;
	}

	.hint {
		font-size: 11px;
		color: var(--text-muted);
		line-height: 1.3;
	}

	.spark {
		margin-top: 4px;
	}

	/* Skeleton */
	.skeleton-label {
		width: 60%;
		height: 14px;
		border-radius: 4px;
		background: var(--surface-hover);
		animation: pulse 1.5s ease-in-out infinite;
	}

	.skeleton-value {
		width: 40%;
		height: 28px;
		border-radius: 4px;
		background: var(--surface-hover);
		animation: pulse 1.5s ease-in-out infinite;
		animation-delay: 0.2s;
	}

	@keyframes pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.5;
		}
	}
</style>
