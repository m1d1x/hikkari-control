<script lang="ts">
	import '../app.css';
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import Sidebar from '$components/layout/Sidebar.svelte';
	import { accessDenied } from '$lib/stores/access';
	import { ensureFreshAdminAccess } from '$lib/auth/refresh';
	import { startKeepalive, stopKeepalive } from '$lib/auth/keepalive';

	let { children } = $props();
	let denied = $state(false);
	// 'booting' covers the initial admin-refresh probe and the very first
	// frame so the dashboard doesn't render with a stale 401 before we
	// know whether the admin session is good.
	let bootState = $state<'booting' | 'ready' | 'login-route'>('booting');
	let currentPath = $state('');
	page.subscribe((p) => {
		currentPath = p.url?.pathname ?? '';
	});

	accessDenied.subscribe((v) => {
		denied = v;
		if (v && currentPath !== '/login') {
			goto('/login', { replaceState: true });
		}
	});

	onMount(async () => {
		// /login is its own auth ceremony: skip the refresh probe so the
		// form works even before any admin cookies exist.
		if (currentPath === '/login') {
			bootState = 'login-route';
			return;
		}

		// Probe the admin session via the independent refresh path. Success
		// = admin cookies are alive; failure = no/dead session, send the
		// operator to the standalone admin login.
		try {
			await ensureFreshAdminAccess();
		} catch {
			await goto('/login', { replaceState: true });
			bootState = 'login-route';
			return;
		}
		bootState = 'ready';
		startKeepalive();
	});

	const ROUTE_TITLES: Record<string, string> = {
		'/login': 'вход',
		'/antiblock': 'антиблок · обзор',
		'/protocols': 'протоколы',
		'/autopilot': 'автопилот',
		'/entry': 'машины',
		'/incidents': 'инциденты',
		'/censor': 'пробник',
		'/domains': 'домены',
		'/providers': 'провайдеры',
		'/metrics': 'метрики продукта',
		'/errors': 'ошибки продукта',
		'/support': 'обращения',
		'/testers': 'логи тестеров',
		'/subscriptions': 'подписки',
		'/donations': 'донаты',
		'/presets': 'пресеты',
		'/groups': 'группы',
		'/security': 'безопасность',
	};

	let pageTitle = $derived(ROUTE_TITLES[currentPath] ?? (currentPath === '/' ? 'обзор' : ''));

	onDestroy(() => {
		stopKeepalive();
	});
</script>

<svelte:head>
	<title>{pageTitle ? `${pageTitle} — hikkari admin` : 'hikkari admin'}</title>
</svelte:head>

{#if bootState === 'login-route'}
	{@render children()}
{:else if bootState === 'booting'}
	<div class="denied-screen">
		<div class="denied-card">
			<div class="boot-spinner" aria-hidden="true"></div>
			<p class="denied-text" style="margin: 0">Подключение…</p>
		</div>
	</div>
{:else if denied}
	<div class="denied-screen">
		<div class="denied-card">
			<svg
				class="denied-icon"
				viewBox="0 0 24 24"
				fill="none"
				stroke="currentColor"
				stroke-width="1.5"
				stroke-linecap="round"
				stroke-linejoin="round"
			>
				<rect x="3" y="11" width="18" height="11" rx="2" ry="2" />
				<path d="M7 11V7a5 5 0 0 1 10 0v4" />
			</svg>
			<h1 class="denied-title">Доступ запрещён</h1>
			<p class="denied-text">
				У вашего аккаунта нет прав для доступа к панели администратора. Убедитесь, что вы вошли с правильного аккаунта.
			</p>
			<button class="denied-btn" onclick={() => location.reload()}> Попробовать снова </button>
		</div>
	</div>
{:else}
	<div class="app">
		<Sidebar />
		{@render children()}
	</div>
{/if}

<style>






	.app {
		display: flex;
		height: 100vh;
		overflow: hidden;
	}

	.denied-screen {
		display: flex;
		align-items: center;
		justify-content: center;
		height: 100vh;
		background: var(--bg);
	}

	.denied-card {
		display: flex;
		flex-direction: column;
		align-items: center;
		max-width: 400px;
		padding: 48px 40px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 12px;
		text-align: center;
	}

	.denied-icon {
		width: 48px;
		height: 48px;
		color: var(--text-muted);
		margin-bottom: 24px;
	}

	.denied-title {
		font-size: 20px;
		font-weight: 600;
		color: var(--text-primary);
		margin: 0 0 12px;
	}

	.denied-text {
		font-size: 14px;
		line-height: 1.6;
		color: var(--text-secondary);
		margin: 0 0 28px;
	}

	.denied-btn {
		padding: 10px 24px;
		font-size: 14px;
		font-weight: 500;
		color: var(--text-primary);
		background: var(--surface-hover);
		border: 1px solid var(--border);
		border-radius: 8px;
		cursor: pointer;
		transition: background 0.15s;
	}

	.denied-btn:hover {
		background: var(--surface-active);
	}

	.boot-spinner {
		width: 28px;
		height: 28px;
		border-radius: 50%;
		border: 2px solid var(--border);
		border-top-color: var(--text-primary);
		animation: boot-spin 0.9s linear infinite;
		margin-bottom: 18px;
	}

	@keyframes boot-spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
