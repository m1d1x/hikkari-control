<script lang="ts">
	import { sidebarCollapsed } from '$lib/stores/sidebar';
	import { icons } from '$components/icons';
	import SidebarSection from './SidebarSection.svelte';
	import SidebarItem from './SidebarItem.svelte';
	import { logoutAdmin } from '$lib/auth/login';

	let collapsed = $derived($sidebarCollapsed);

	async function signOut() {
		await logoutAdmin();
		// A full load, not goto: it drops the keepalive and alert timers and
		// every store along with the page, so nothing keeps knocking with
		// the dead session.
		location.replace('/login');
	}
</script>

<aside class="sidebar" class:collapsed>
	<div class="header">
		{#if !collapsed}
			<span class="logo">NEKKO</span>
			<span class="badge">admin</span>
		{/if}
		<button class="toggle" onclick={() => sidebarCollapsed.toggle()} title="Toggle sidebar">
			{@html icons.collapse}
		</button>
	</div>

	<div class="nav">
		<!-- Anti-blocking is one subsystem, not a drawer of loose pages (plan 42
		     П9.9 §6а): the overview only observes, the screens below it only act. -->
		<SidebarSection label="Антиблок" {collapsed}>
			<SidebarItem href="/antiblock" icon="overview" label="Обзор" {collapsed} />
			<SidebarItem href="/autopilot" icon="jobs" label="Автопилот" {collapsed} />
			<SidebarItem href="/entry" icon="server" label="Машины" {collapsed} />
			<SidebarItem href="/incidents" icon="events" label="Инциденты" {collapsed} />
			<SidebarItem href="/censor" icon="realtime" label="Пробник" {collapsed} />
			<SidebarItem href="/domains" icon="search" label="Домены" {collapsed} />
			<SidebarItem href="/providers" icon="system" label="Провайдеры" {collapsed} />
		</SidebarSection>

		<SidebarSection label="Продукт" {collapsed}>
			<SidebarItem href="/metrics" icon="overview" label="Обзор" {collapsed} />
			<SidebarItem href="/errors" icon="events" label="Ошибки" {collapsed} />
			<SidebarItem href="/support" icon="search" label="Обращения" {collapsed} />
			<SidebarItem href="/donations" icon="jobs" label="Донаты" {collapsed} />
			<SidebarItem href="/presets" icon="system" label="Пресеты" {collapsed} />
			<SidebarItem href="/groups" icon="system" label="Группы" {collapsed} />
		</SidebarSection>

		<SidebarSection label="Staff" {collapsed}>
			<SidebarItem href="/security" icon="debug" label="Security" {collapsed} />
		</SidebarSection>
	</div>

	<button class="sign-out" class:collapsed onclick={signOut} title={collapsed ? 'Sign out' : undefined}>
		<span class="icon">{@html icons.logout}</span>
		{#if !collapsed}
			<span>Sign out</span>
		{/if}
	</button>
</aside>

<style>
	.sidebar {
		display: flex;
		flex-direction: column;
		width: var(--sidebar-width);
		min-width: var(--sidebar-width);
		height: 100vh;
		background: var(--sidebar-bg);
		border-right: 1px solid var(--border-subtle);
		transition:
			width var(--transition-normal),
			min-width var(--transition-normal);
		overflow: hidden;
	}

	.sidebar.collapsed {
		width: var(--sidebar-collapsed-width);
		min-width: var(--sidebar-collapsed-width);
	}

	.header {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 16px 12px;
		border-bottom: 1px solid var(--border-subtle);
		min-height: 56px;
	}

	.logo {
		font-size: 16px;
		font-weight: 700;
		letter-spacing: 0.04em;
		color: var(--text-primary);
	}

	.badge {
		font-size: 10px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--primary);
		background: var(--primary-subtle);
		padding: 2px 6px;
		border-radius: 4px;
	}

	.toggle {
		display: flex;
		align-items: center;
		justify-content: center;
		margin-left: auto;
		padding: 4px;
		color: var(--text-secondary);
		border-radius: 4px;
		transition: color var(--transition-fast);
	}

	.toggle:hover {
		color: var(--text-primary);
	}

	.collapsed .toggle {
		margin: 0 auto;
	}

	.nav {
		flex: 1;
		overflow-y: auto;
		padding: 12px 8px;
		display: flex;
		flex-direction: column;
		gap: 16px;
	}

	.sign-out {
		display: flex;
		align-items: center;
		gap: 10px;
		margin: 8px;
		padding: 8px 12px;
		border-radius: 6px;
		color: var(--text-secondary);
		transition: all var(--transition-fast);
	}

	.sign-out:hover {
		color: var(--text-primary);
		background: var(--sidebar-item-hover);
	}

	.sign-out.collapsed {
		justify-content: center;
		padding: 8px;
	}

	.sign-out .icon {
		display: flex;
		flex-shrink: 0;
	}
</style>
