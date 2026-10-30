<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		label: string;
		collapsed?: boolean;
		children: Snippet;
	}

	let { label, collapsed = false, children }: Props = $props();
</script>

<div class="section">
	{#if !collapsed}
		<span class="label">{label}</span>
	{:else}
		<div class="divider"></div>
	{/if}
	<nav class="items">
		{@render children()}
	</nav>
</div>

<style>
	.section {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.label {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
		padding: 8px 12px 4px;
	}

	.divider {
		height: 1px;
		background: var(--border-subtle);
		margin: 4px 8px;
	}

	.items {
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
</style>
