<script lang="ts">
	interface Props {
		message?: string;
		onRetry?: () => void;
	}

	let { message = 'Something went wrong', onRetry }: Props = $props();
</script>

<div class="error-state">
	<p class="message">{message}</p>
	{#if onRetry}
		<button class="retry" onclick={onRetry}>Retry</button>
	{/if}
</div>

<style>
	.error-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 12px;
		padding: 48px 24px;
	}

	.message {
		color: var(--status-critical);
		font-size: 14px;
	}

	.retry {
		font-size: 13px;
		font-weight: 500;
		color: var(--text-primary);
		padding: 6px 16px;
		border: 1px solid var(--border);
		border-radius: 6px;
		transition: all var(--transition-fast);
	}

	.retry:hover {
		background: var(--surface-hover);
	}
</style>
