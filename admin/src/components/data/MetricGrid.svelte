<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		columns?: number;
		children: Snippet;
	}

	let { columns = 4, children }: Props = $props();
</script>

<div class="metric-grid" style="--cols: {columns}">
	{@render children()}
</div>

<style>
	.metric-grid {
		display: grid;
		grid-template-columns: repeat(var(--cols), 1fr);
		gap: var(--card-gap);
	}

	@media (max-width: 1200px) {
		.metric-grid {
			grid-template-columns: repeat(3, 1fr);
		}
	}

	@media (max-width: 900px) {
		.metric-grid {
			grid-template-columns: repeat(2, 1fr);
		}
	}

	@media (max-width: 600px) {
		.metric-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
