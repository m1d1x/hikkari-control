<script lang="ts">
	import { formatDuration, formatBytes, formatNumber, formatRelativeTime, formatDate } from '$lib/utils/format';
	import { shadowbanUser, banUser, unbanUser, deleteUser } from '$lib/api/analytics';
	import { describeApiError } from '$lib/api/client';
	import { fetchBoostStatus, setBoost, clearBoost } from '$lib/api/boost';
	import { cancelErasure } from '$lib/api/security';
	import type { UserListItem } from '$lib/types/analytics';

	interface Props {
		user: UserListItem;
		onclick?: () => void;
		onUpdate?: () => void;
	}

	let { user, onclick, onUpdate }: Props = $props();

	const shortId = $derived(user.id.slice(0, 8));
	const lastSeenText = $derived(user.lastSeen ? formatRelativeTime(user.lastSeen) : 'never');

	let confirmAction: 'shadow' | 'ban' | 'unban' | 'delete' | null = $state(null);
	let actionError = $state<string | null>(null);
	let actionLoading = $state(false);

	// A pending erasure replaces the moderation strip with its undo and a
	// countdown; the clock ticks once a minute, which is all "about 6d 23h" needs.
	let now = $state(Date.now());
	$effect(() => {
		if (!user.eraseAt) return;
		const t = setInterval(() => (now = Date.now()), 60_000);
		return () => clearInterval(t);
	});
	const erasesIn = $derived(user.eraseAt ? formatCountdown(new Date(user.eraseAt).getTime() - now) : '');
	let undoLoading = $state(false);
	let undoError = $state<string | null>(null);

	function formatCountdown(ms: number): string {
		if (ms <= 0) return 'now';
		const m = Math.floor(ms / 60_000);
		const d = Math.floor(m / 1440);
		const h = Math.floor((m % 1440) / 60);
		if (d > 0) return `${d}d ${h}h`;
		if (h > 0) return `${h}h ${m % 60}m`;
		return `${Math.max(m, 1)}m`;
	}

	async function undoErasure() {
		if (undoLoading) return;
		undoLoading = true;
		undoError = null;
		try {
			await cancelErasure(user.id);
			onUpdate?.();
		} catch (e) {
			undoError = describeApiError(e);
		} finally {
			undoLoading = false;
		}
	}

	let boostActive = $state(false);
	let boostExpiresAt: string | undefined = $state(undefined);
	let boostLoading = $state(false);

	$effect(() => {
		// Refresh boost state whenever the underlying user changes (paginated list reuses cards).
		const id = user.id;
		fetchBoostStatus(id)
			.then((s) => {
				if (user.id !== id) return;
				boostActive = !!s.active;
				boostExpiresAt = s.expiresAt;
			})
			.catch(() => {});
	});

	function formatExpires(iso?: string): string {
		if (!iso) return '';
		const d = new Date(iso);
		if (isNaN(d.getTime())) return '';
		const pad = (n: number) => String(n).padStart(2, '0');
		return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
	}

	async function toggleBoost() {
		if (boostLoading) return;
		boostLoading = true;
		try {
			const res = boostActive ? await clearBoost(user.id) : await setBoost(user.id, 60);
			boostActive = !!res.active;
			boostExpiresAt = res.expiresAt;
		} catch (e) {
			console.error('Boost toggle failed:', e);
		} finally {
			boostLoading = false;
		}
	}

	function fraudColor(score: number): string {
		if (score <= 30) return 'var(--status-healthy)';
		if (score <= 60) return 'var(--status-warning)';
		return 'var(--status-critical)';
	}

	function stopProp(e: Event) {
		e.stopPropagation();
	}

	async function executeAction() {
		if (!confirmAction) return;
		actionLoading = true;
		try {
			if (confirmAction === 'shadow') await shadowbanUser(user.id);
			else if (confirmAction === 'ban') await banUser(user.id);
			else if (confirmAction === 'unban') await unbanUser(user.id);
			else if (confirmAction === 'delete') await deleteUser(user.id);
			onUpdate?.();
			confirmAction = null;
		} catch (e) {
			// A refusal (role, already scheduled) is said, not swallowed.
			actionError = describeApiError(e);
		} finally {
			actionLoading = false;
		}
	}
</script>

<div class="row">
	<button class="card" {onclick} type="button">
		{#if user.eraseAt}
			<span class="status-badge banned" title="Erased {new Date(user.eraseAt).toLocaleString()}"
				>DELETING · {erasesIn}</span
			>
		{:else if user.isBanned}
			<span class="status-badge banned">BAN</span>
		{:else if user.isShadowbanned}
			<span class="status-badge shadow">SHADOW</span>
		{:else if boostActive}
			<span
				class="status-badge boost"
				title={boostExpiresAt ? `Boost active until ${formatExpires(boostExpiresAt)}` : 'Boost active'}>BOOST</span
			>
		{/if}

		<span class="cell c-username">{user.username}</span>
		<span class="cell c-id mono dim">{shortId}</span>
		<span class="cell c-lastseen dim">{lastSeenText}</span>
		<span class="cell c-avg mono">{user.avgSessionMs ? formatDuration(user.avgSessionMs) : '—'}</span>
		<span class="cell c-max mono">{user.maxSessionMs ? formatDuration(user.maxSessionMs) : '—'}</span>
		<span class="cell c-sticky mono">
			{user.stickiness ? user.stickiness.toFixed(1) + '%' : '—'}
		</span>
		<span class="cell c-s3 mono">{user.s3StorageBytes ? formatBytes(user.s3StorageBytes) : '—'}</span>
		<span class="cell c-msgs mono">
			{user.totalMessages ? formatNumber(user.totalMessages) : '—'}
		</span>
		<span class="cell c-servers mono">{user.serverCount || '—'}</span>
		<span class="cell c-created mono dim">{formatDate(user.createdAt)}</span>
		<span class="cell c-fraud mono" style:color={fraudColor(user.fraudScore)}>
			{user.fraudScore}
		</span>
	</button>

	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="actions" onclick={stopProp}>
		<button
			class="act-btn act-boost"
			class:act-boost-on={boostActive}
			disabled={boostLoading}
			title={boostActive
				? `Cancel boost${boostExpiresAt ? ' (until ' + formatExpires(boostExpiresAt) + ')' : ''}`
				: 'Boost debug/trace logs for 1h'}
			onclick={toggleBoost}
		>
			{boostLoading ? '…' : boostActive ? 'Unboost' : 'Boost'}
		</button>

		{#if user.eraseAt}
			<button
				class="act-btn act-unban"
				title={undoError ?? 'Cancel the deletion and restore the account'}
				disabled={undoLoading}
				onclick={undoErasure}
			>
				{undoLoading ? '…' : 'Undelete'}
			</button>
		{:else}
			{#if user.isShadowbanned}
				<button class="act-btn act-unshadow" title="Remove shadowban" onclick={() => (confirmAction = 'shadow')}>
					Unshadow
				</button>
			{:else}
				<button
					class="act-btn act-shadow"
					title="Shadowban: user can post but nobody sees"
					onclick={() => (confirmAction = 'shadow')}
				>
					Shadow
				</button>
			{/if}

			{#if user.isBanned}
				<button class="act-btn act-unban" title="Unban user" onclick={() => (confirmAction = 'unban')}> Unban </button>
			{:else}
				<button
					class="act-btn act-ban"
					title="Ban: account locked, cannot login"
					onclick={() => (confirmAction = 'ban')}
				>
					Ban
				</button>
			{/if}

			<button
				class="act-btn act-delete"
				title="Delete: permanently remove user and all data"
				onclick={() => (confirmAction = 'delete')}
			>
				Delete
			</button>
		{/if}
	</div>

	{#if confirmAction}
		<!-- svelte-ignore a11y_click_events_have_key_events -->
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div class="confirm-overlay" onclick={stopProp}>
			<div class="confirm-box">
				<span class="confirm-text">
					{#if confirmAction === 'shadow'}
						{user.isShadowbanned ? 'Remove shadowban from' : 'Shadowban'}
						<strong>{user.username}</strong>?
					{:else if confirmAction === 'ban'}
						Ban <strong>{user.username}</strong>? They will be disconnected and unable to login.
					{:else if confirmAction === 'unban'}
						Unban <strong>{user.username}</strong>?
					{:else if confirmAction === 'delete'}
						Delete <strong>{user.username}</strong> in 7 days? The account is blocked and signed out now; until then
						<em>Undelete</em> on this row brings it back.
					{/if}
				</span>
				{#if actionError}<span class="confirm-error">{actionError}</span>{/if}
				<div class="confirm-actions">
					<button
						class="confirm-btn confirm-cancel"
						onclick={() => ((confirmAction = null), (actionError = null))}
						disabled={actionLoading}
					>
						Cancel
					</button>
					<button
						class="confirm-btn confirm-yes {confirmAction === 'delete' ? 'confirm-danger' : ''}"
						onclick={executeAction}
						disabled={actionLoading}
					>
						{actionLoading ? '...' : 'Confirm'}
					</button>
				</div>
			</div>
		</div>
	{/if}
</div>

<style>
	/*
	 * The row is two tracks: the data grid (from --users-grid, shared with the
	 * page's header) and the action strip. The buttons used to be absolutely
	 * positioned over a 300px right padding, which is why the columns rode over
	 * them on a narrow window — now the strip is a real track nothing can enter.
	 */
	.row {
		position: relative;
		display: grid;
		grid-template-columns: 1fr var(--users-actions-w);
		align-items: center;
		gap: 12px;
	}

	.card {
		display: grid;
		grid-template-columns: var(--users-grid);
		align-items: center;
		gap: 0 10px;
		width: 100%;
		min-width: 0;
		padding: 11px 16px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
		text-align: left;
		cursor: pointer;
		transition:
			background var(--transition-fast),
			border-color var(--transition-fast);
	}

	.card:hover {
		background: var(--surface-hover);
		border-color: var(--border);
	}

	/* Status badge */
	.status-badge {
		position: absolute;
		left: -1px;
		top: -1px;
		font-size: 9px;
		font-weight: 700;
		letter-spacing: 0.06em;
		padding: 2px 6px;
		border-radius: 6px 0 6px 0;
		line-height: 1;
	}

	.status-badge.banned {
		background: var(--status-critical);
		color: #fff;
	}

	.status-badge.shadow {
		background: var(--status-warning);
		color: #000;
	}

	.status-badge.boost {
		background: var(--status-healthy, #3fb950);
		color: #000;
	}

	/* Cells */
	.cell {
		font-size: 12px;
		color: var(--text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}

	.c-username {
		font-size: 13px;
		font-weight: 600;
	}

	.c-id,
	.c-lastseen,
	.c-created {
		font-size: 11px;
	}

	.mono {
		font-family: var(--font-mono);
	}

	.dim {
		color: var(--text-secondary);
	}

	/* Action buttons */
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: 6px;
	}

	.act-btn {
		padding: 4px 10px;
		font-size: 11px;
		font-weight: 600;
		border: 1px solid var(--border);
		border-radius: 4px;
		background: var(--surface);
		cursor: pointer;
		transition: all var(--transition-fast);
		white-space: nowrap;
	}

	.act-boost {
		color: var(--status-healthy, #3fb950);
	}

	.act-boost:hover:not(:disabled) {
		background: var(--status-healthy, #3fb950);
		color: #000;
		border-color: var(--status-healthy, #3fb950);
	}

	.act-boost-on {
		background: var(--status-healthy, #3fb950);
		color: #000;
		border-color: var(--status-healthy, #3fb950);
	}

	.act-boost-on:hover:not(:disabled) {
		background: transparent;
		color: var(--status-healthy, #3fb950);
	}

	.act-shadow {
		color: var(--status-warning);
	}

	.act-shadow:hover {
		background: var(--status-warning);
		color: #000;
		border-color: var(--status-warning);
	}

	.act-unshadow {
		color: var(--text-secondary);
	}

	.act-unshadow:hover {
		background: var(--surface-hover);
	}

	.act-ban {
		color: var(--status-critical);
	}

	.act-ban:hover {
		background: var(--status-critical);
		color: #fff;
		border-color: var(--status-critical);
	}

	.act-unban {
		color: var(--status-healthy);
	}

	.act-unban:hover {
		background: var(--status-healthy);
		color: #fff;
		border-color: var(--status-healthy);
	}

	.act-delete {
		color: var(--status-critical);
	}

	.act-delete:hover {
		background: var(--status-critical);
		color: #fff;
		border-color: var(--status-critical);
	}

	/* Confirm overlay */
	.confirm-overlay {
		position: absolute;
		inset: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		background: rgba(0, 0, 0, 0.75);
		border-radius: 6px;
		z-index: 10;
	}

	.confirm-box {
		display: flex;
		align-items: center;
		gap: 16px;
		padding: 10px 16px;
	}

	.confirm-text {
		font-size: 12px;
		color: var(--text-primary);
	}

	.confirm-error {
		font-size: 12px;
		color: #e25555;
	}

	.confirm-actions {
		display: flex;
		gap: 8px;
	}

	.confirm-btn {
		padding: 5px 14px;
		font-size: 11px;
		font-weight: 600;
		border-radius: 4px;
		border: 1px solid var(--border);
		cursor: pointer;
		transition: all var(--transition-fast);
	}

	.confirm-cancel {
		background: var(--surface);
		color: var(--text-secondary);
	}

	.confirm-cancel:hover {
		background: var(--surface-hover);
	}

	.confirm-yes {
		background: var(--primary);
		color: #fff;
		border-color: var(--primary);
	}

	.confirm-yes:hover {
		opacity: 0.9;
	}

	.confirm-danger {
		background: var(--status-critical);
		border-color: var(--status-critical);
	}

	.confirm-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
</style>
