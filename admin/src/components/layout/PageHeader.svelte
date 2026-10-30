<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title: string;
		description?: string;
		actions?: Snippet;
	}

	let { title, description, actions }: Props = $props();
</script>

<header class="page-header">
	<div class="info">
		<h1 class="title">{title}</h1>
		{#if description}
			<p class="description">{description}</p>
		{/if}
	</div>
	{#if actions}
		<div class="actions">
			{@render actions()}
		</div>
	{/if}
</header>

<style>
	.page-header {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 16px;
		padding-bottom: 20px;
		border-bottom: 1px solid var(--border-subtle);
		margin-bottom: 24px;
	}

	.title {
		font-size: 20px;
		font-weight: 600;
		color: var(--text-primary);
		line-height: 1.3;
	}

	.description {
		font-size: 13px;
		color: var(--text-secondary);
		margin-top: 4px;
	}

	.actions {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-shrink: 0;
	}
</style>
