<script lang="ts">
	import { page } from '$app/stores';
	import type { IconName } from '$components/icons';
	import { icons } from '$components/icons';

	interface Props {
		href: string;
		icon: IconName;
		label: string;
		collapsed?: boolean;
	}

	let { href, icon, label, collapsed = false }: Props = $props();

	let active = $derived($page.url.pathname.startsWith(href));
</script>

<a {href} class="item" class:active class:collapsed title={collapsed ? label : undefined}>
	<span class="icon">{@html icons[icon]}</span>
	{#if !collapsed}
		<span class="label">{label}</span>
	{/if}
</a>

<style>
	.item {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 8px 12px;
		border-radius: 6px;
		color: var(--text-secondary);
		transition: all var(--transition-fast);
		position: relative;
	}

	.item:hover {
		color: var(--text-primary);
		background: var(--sidebar-item-hover);
	}

	.item.active {
		color: var(--text-primary);
		background: var(--sidebar-item-active);
	}

	.item.active::before {
		content: '';
		position: absolute;
		left: 0;
		top: 6px;
		bottom: 6px;
		width: 3px;
		border-radius: 0 2px 2px 0;
		background: var(--primary);
	}

	.item.collapsed {
		justify-content: center;
		padding: 8px;
	}

	.icon {
		display: flex;
		align-items: center;
		flex-shrink: 0;
	}

	.label {
		font-size: 13px;
		font-weight: 500;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
