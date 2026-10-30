<script lang="ts">
	import type { MailerConfig } from '$lib/types/mailer';
	import { setMailerProvider } from '$lib/api/mailer';

	interface Props {
		config: MailerConfig | null;
		loading: boolean;
		// Called after a successful provider switch so the parent page can
		// refresh its cached config (badge label, history filtering, etc).
		onSwitched?: (next: MailerConfig) => void;
	}

	let { config, loading, onSwitched }: Props = $props();

	const labels: Record<string, string> = {
		unisender: 'Unisender Go',
		smtp: 'Selectel SMTP',
	};

	const providerLabel = $derived(config ? (labels[config.provider] ?? config.provider) : '');

	// Switch state — local to the badge so the parent doesn't need to know
	// about the in-flight RPC. The dropdown is hidden until the user clicks
	// the bezel; the active option is omitted from the choices.
	let menuOpen = $state(false);
	let switching = $state(false);
	let switchError = $state<string | null>(null);

	function toggleMenu() {
		if (loading || switching) return;
		menuOpen = !menuOpen;
		switchError = null;
	}

	function closeMenu() {
		menuOpen = false;
	}

	async function pick(next: 'smtp' | 'unisender') {
		if (!config || next === config.provider) {
			menuOpen = false;
			return;
		}
		menuOpen = false;
		switching = true;
		switchError = null;
		try {
			const resp = await setMailerProvider(next);
			const updated: MailerConfig = { ...config, provider: resp.provider as 'smtp' | 'unisender' };
			config = updated;
			onSwitched?.(updated);
		} catch (e: unknown) {
			switchError = e instanceof Error ? e.message : String(e);
		} finally {
			switching = false;
		}
	}

	// availableTargets returns the providers the operator can switch TO —
	// excludes the active one and any backend the upstream node doesn't
	// have credentials for.
	const availableTargets = $derived.by(() => {
		if (!config) return [];
		const out: Array<{ id: 'smtp' | 'unisender'; label: string; configured: boolean }> = [];
		for (const id of ['unisender', 'smtp'] as const) {
			if (id === config.provider) continue;
			const configured =
				id === 'unisender' ? config.unisender_configured : config.smtp_configured;
			out.push({ id, label: labels[id], configured });
		}
		return out;
	});
</script>

<svelte:window onclick={() => menuOpen && closeMenu()} />

<div class="badge-row">
	{#if loading}
		<span class="badge skeleton">Загрузка…</span>
	{:else if !config}
		<span class="badge unknown">Провайдер неизвестен</span>
	{:else}
		<div class="dropdown" onclick={(e) => e.stopPropagation()} role="presentation">
			<button
				type="button"
				class="badge active dropdown-trigger"
				data-provider={config.provider}
				disabled={switching}
				onclick={toggleMenu}
				aria-haspopup="listbox"
				aria-expanded={menuOpen}
			>
				<span class="dot"></span>
				<span>{providerLabel}</span>
				{#if switching}
					<span class="mini-spinner" aria-hidden="true"></span>
				{:else}
					<span class="caret" aria-hidden="true">▾</span>
				{/if}
			</button>

			{#if menuOpen}
				<div class="menu" role="listbox">
					<div class="menu-header">Переключить провайдер</div>
					{#each availableTargets as t (t.id)}
						<button
							type="button"
							role="option"
							aria-selected="false"
							class="menu-item"
							disabled={!t.configured}
							onclick={() => pick(t.id)}
							title={t.configured ? '' : 'Не настроены ENV-переменные на mainru'}
						>
							<span class="menu-dot" data-provider={t.id}></span>
							<span class="menu-label">{t.label}</span>
							{#if !t.configured}
								<span class="menu-tag">env missing</span>
							{/if}
						</button>
					{/each}
					{#if availableTargets.length === 0}
						<div class="menu-empty">Других сконфигурированных бэкендов нет</div>
					{/if}
				</div>
			{/if}
		</div>

		<span class="from" title="From: {config.from_address}">
			{config.from_address}
		</span>

		<span class="alt-label">Доступно:</span>
		<span class="alt-pills">
			<span class="pill" class:on={config.unisender_configured}>Unisender Go</span>
			<span class="pill" class:on={config.smtp_configured}>SMTP</span>
		</span>

		{#if switchError}
			<span class="switch-error">{switchError}</span>
		{/if}
	{/if}
</div>

<style>
	.badge-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 10px;
		margin-bottom: 14px;
		font-size: 13px;
	}

	.dropdown {
		position: relative;
		display: inline-flex;
	}

	.badge,
	.dropdown-trigger {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 5px 12px;
		font-size: 12px;
		font-weight: 600;
		border-radius: 999px;
		background: var(--primary-subtle);
		color: var(--text-primary);
		border: 1px solid var(--primary-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		cursor: pointer;
		font-family: inherit;
	}

	.dropdown-trigger:disabled {
		cursor: not-allowed;
		opacity: 0.7;
	}

	.dropdown-trigger:hover:not(:disabled) {
		filter: brightness(1.05);
	}

	.badge.active[data-provider='unisender'],
	.dropdown-trigger[data-provider='unisender'] {
		background: rgba(80, 160, 110, 0.15);
		border-color: rgba(80, 160, 110, 0.45);
	}

	.badge.active[data-provider='smtp'],
	.dropdown-trigger[data-provider='smtp'] {
		background: rgba(120, 130, 200, 0.15);
		border-color: rgba(120, 130, 200, 0.45);
	}

	.badge.skeleton {
		opacity: 0.5;
	}

	.badge.unknown {
		background: rgba(150, 150, 150, 0.12);
		border-color: var(--border-subtle);
		color: var(--text-secondary);
	}

	.dot {
		display: inline-block;
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: currentColor;
	}

	.caret {
		font-size: 9px;
		opacity: 0.7;
	}

	.mini-spinner {
		display: inline-block;
		width: 10px;
		height: 10px;
		border: 1.5px solid currentColor;
		border-right-color: transparent;
		border-radius: 50%;
		animation: spin 0.6s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	.menu {
		position: absolute;
		top: calc(100% + 6px);
		left: 0;
		min-width: 220px;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 8px;
		box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
		padding: 6px;
		z-index: 50;
	}

	.menu-header {
		padding: 6px 10px 8px;
		font-size: 11px;
		font-weight: 600;
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
		border-bottom: 1px solid var(--border-subtle);
		margin-bottom: 4px;
	}

	.menu-item {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		padding: 8px 10px;
		border: none;
		border-radius: 6px;
		background: transparent;
		color: var(--text-primary);
		text-align: left;
		font-size: 13px;
		cursor: pointer;
		font-family: inherit;
	}

	.menu-item:hover:not(:disabled) {
		background: var(--primary-subtle);
	}

	.menu-item:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.menu-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
	}

	.menu-dot[data-provider='unisender'] {
		background: rgba(80, 160, 110, 0.9);
	}

	.menu-dot[data-provider='smtp'] {
		background: rgba(120, 130, 200, 0.9);
	}

	.menu-label {
		flex: 1;
	}

	.menu-tag {
		font-size: 10px;
		color: var(--text-muted);
		font-family: var(--font-mono);
		text-transform: uppercase;
	}

	.menu-empty {
		padding: 8px 10px;
		font-size: 12px;
		color: var(--text-muted);
	}

	.from {
		font-family: var(--font-mono);
		font-size: 12px;
		color: var(--text-secondary);
	}

	.alt-label {
		margin-left: 4px;
		font-size: 11px;
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	.alt-pills {
		display: inline-flex;
		gap: 6px;
	}

	.pill {
		padding: 2px 8px;
		font-size: 11px;
		border-radius: 4px;
		border: 1px solid var(--border-subtle);
		color: var(--text-muted);
		background: transparent;
	}

	.pill.on {
		color: var(--text-primary);
		border-color: var(--primary-muted);
		background: var(--primary-subtle);
	}

	.switch-error {
		font-size: 12px;
		color: var(--status-critical);
		flex-basis: 100%;
	}
</style>
