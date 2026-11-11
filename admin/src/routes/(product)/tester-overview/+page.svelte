<script lang="ts">
	import WinChips from '$components/product/WinChips.svelte';
	import TesterList from '$components/product/TesterList.svelte';
	import { fetchTesterCharts } from '$lib/api/product';
	import { Windowed } from '$lib/product/windowed.svelte';

	// Each tester on their own: the minutes their tunnel was on and carried
	// nothing next to their own network to Russia (ya.ru, vk.com over the
	// direct path), so a broken tunnel reads apart from a broken home network.
	// The list shows the latest of both; a row opens its two charts.
	const m = new Windowed('24h', fetchTesterCharts);
</script>

<div class="head">
	<div class="head-info">
		<h1>Тестеры</h1>
	</div>
	<div class="head-controls">
		{#if m.loading && m.data}<span class="dim loading-text">обновление…</span>{/if}
		<WinChips bind:value={m.win} />
	</div>
</div>

{#if m.error}
	<div class="error-banner">Ошибка загрузки: {m.error}</div>
{/if}

<TesterList charts={m.data} />

<style>
	.head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 16px;
		margin-bottom: 24px;
	}

	.head-info h1 {
		font-size: 22px;
		font-weight: 700;
		letter-spacing: -0.02em;
		color: var(--text);
		margin: 0;
	}

	.head-controls {
		display: flex;
		align-items: center;
		gap: 12px;
	}

	.loading-text {
		font-size: 12px;
	}

	.error-banner {
		padding: 12px 16px;
		margin-bottom: 20px;
		border-radius: 8px;
		background: rgba(220, 38, 38, 0.15);
		border: 1px solid rgba(220, 38, 38, 0.3);
		color: #f87171;
		font-size: 13px;
	}
</style>
