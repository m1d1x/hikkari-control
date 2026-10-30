<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		children: Snippet;
	}

	let { children }: Props = $props();
</script>

<main class="page-shell">
	<div class="content">
		{@render children()}
	</div>
</main>

<style>
	.page-shell {
		flex: 1;
		overflow-y: auto;
		overflow-x: hidden;
		height: 100vh;
	}

	.content {
		padding: var(--page-padding);
		max-width: 1600px;
	}
</style>
