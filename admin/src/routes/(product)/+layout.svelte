<script lang="ts">
	// Product screens. Metrics and errors read the hub (client telemetry),
	// groups and presets its catalog, subscriptions its billing; the rest still render $lib/product/mock
	// and say so.
	import { page } from '$app/state';
	import '$lib/styles/product.css';

	const REAL = ['/metrics', '/errors', '/groups', '/presets', '/subscriptions', '/support', '/testers'];
	const mock = $derived(!REAL.includes(page.url.pathname));

	let { children } = $props();
</script>

<main class="product">
	{#if mock}<div class="mock-note">Макет: данные демонстрационные, бэкенд для этого раздела ещё не сделан.</div>{/if}
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
