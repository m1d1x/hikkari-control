<script lang="ts">
	// Product screens carried over as-is from the Hikkari admin mock: every
	// figure comes from $lib/product/mock, no hub endpoint feeds them yet.
	import '$lib/styles/product.css';

	let { children } = $props();
</script>

<main class="product">
	<div class="mock-note">Макет: данные демонстрационные, бэкенд для этого раздела ещё не сделан.</div>
	{@render children()}
</main>

<style>
	.mock-note {
		margin-bottom: 16px;
		padding: 8px 12px;
		border: 1px solid var(--warn);
		border-radius: 6px;
		color: var(--warn);
		font-size: 12px;
	}
</style>
