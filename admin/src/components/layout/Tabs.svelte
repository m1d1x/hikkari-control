<script lang="ts" module>
	export interface TabDef {
		key: string;
		label: string;
	}

	/**
	 * Resolves which tab a page should open on. The query string is the source
	 * of truth so a deep link, a reload and the back button all land on the tab
	 * the operator was looking at; anything unknown falls back to the first tab.
	 */
	export function tabFromUrl(url: URL, tabs: readonly TabDef[], param = 'tab'): string {
		const requested = url.searchParams.get(param);
		return requested && tabs.some((t) => t.key === requested) ? requested : tabs[0].key;
	}
</script>

<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';

	interface Props {
		tabs: readonly TabDef[];
		active: string;
		/** Query-string key the tab is mirrored into. */
		param?: string;
	}

	let { tabs, active = $bindable(), param = 'tab' }: Props = $props();

	function select(key: string) {
		if (key === active) return;
		active = key;
		const url = new URL(page.url);
		// The first tab is the page default, so it stays off the query string.
		if (key === tabs[0].key) url.searchParams.delete(param);
		else url.searchParams.set(param, key);
		goto(url.pathname + url.search, { replaceState: true, noScroll: true, keepFocus: true });
	}
</script>

<div class="tabs" role="tablist">
	{#each tabs as t (t.key)}
		<button
			type="button"
			role="tab"
			aria-selected={active === t.key}
			class="tab"
			class:active={active === t.key}
			onclick={() => select(t.key)}
		>
			{t.label}
		</button>
	{/each}
</div>

<style>
	.tabs {
		display: flex;
		gap: 4px;
		flex-wrap: wrap;
		margin-bottom: 20px;
		border-bottom: 1px solid var(--border-subtle);
	}

	.tab {
		padding: 8px 14px;
		font: inherit;
		font-size: 13px;
		font-weight: 500;
		color: var(--text-secondary);
		background: transparent;
		border: none;
		border-bottom: 2px solid transparent;
		margin-bottom: -1px;
		cursor: pointer;
		transition:
			color var(--transition-fast),
			border-color var(--transition-fast);
	}

	.tab:hover {
		color: var(--text-primary);
	}

	.tab.active {
		color: var(--text-primary);
		border-bottom-color: var(--primary);
	}
</style>
