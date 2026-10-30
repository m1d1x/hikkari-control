<script lang="ts">
	import { formatDate, formatRelativeTime } from '$lib/utils/format';
	import type { UserSessionInfo } from '$lib/types/analytics';

	let { sessions }: { sessions: UserSessionInfo[] } = $props();

	function short(value: string | null | undefined, left = 12, right = 8): string {
		if (!value) return '-';
		if (value.length <= left + right + 3) return value;
		return `${value.slice(0, left)}...${value.slice(-right)}`;
	}

	function statusLabel(session: UserSessionInfo): string {
		if (session.isActive) return 'Active';
		if (session.revokedAt) return session.revokedReason || 'Revoked';
		return 'Expired';
	}
</script>

<div class="sessions">
	{#if sessions.length === 0}
		<p class="empty-text">No saved sessions</p>
	{:else}
		{#each sessions as session (session.id)}
			<div class="session-row" class:inactive={!session.isActive}>
				<div class="session-main">
					<div class="session-title">
						<span>{session.platform || 'unknown'}</span>
						<span class:active={session.isActive} class="status">{statusLabel(session)}</span>
						{#if session.isTrusted}
							<span class="trusted">Trusted</span>
						{/if}
					</div>
					<div class="session-meta">
						<span>Last used: {formatRelativeTime(session.lastUsedAt)}</span>
						<span>Created: {formatDate(session.createdAt)}</span>
						<span>Expires: {formatDate(session.expiresAt)}</span>
					</div>
				</div>

				<div class="session-grid">
					<div>
						<span class="label">IP</span>
						<code>{session.lastIp || '-'}</code>
					</div>
					<div>
						<span class="label">Fingerprint</span>
						<code title={session.fingerprintHash}>{short(session.fingerprintHash)}</code>
					</div>
					<div>
						<span class="label">Public key</span>
						<code title={session.publicKey}>{short(session.publicKey)}</code>
					</div>
					<div class="ua">
						<span class="label">User agent</span>
						<code title={session.lastUserAgent || ''}>{short(session.lastUserAgent, 42, 14)}</code>
					</div>
				</div>
			</div>
		{/each}
	{/if}
</div>

<style>
	.sessions {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.session-row {
		display: grid;
		grid-template-columns: minmax(240px, 0.8fr) minmax(320px, 1.2fr);
		gap: 16px;
		padding: 14px;
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		background: var(--card-bg);
	}

	.session-row.inactive {
		opacity: 0.72;
	}

	.session-main {
		min-width: 0;
	}

	.session-title {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 14px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.status,
	.trusted {
		padding: 2px 6px;
		border-radius: 4px;
		font-size: 10px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		background: var(--surface);
		color: var(--text-muted);
	}

	.status.active {
		background: oklch(0.55 0.15 145 / 0.16);
		color: var(--status-healthy);
	}

	.trusted {
		background: oklch(0.7 0.12 85 / 0.15);
		color: var(--status-warning);
	}

	.session-meta {
		display: flex;
		flex-wrap: wrap;
		gap: 8px 14px;
		margin-top: 8px;
		font-size: 12px;
		color: var(--text-muted);
	}

	.session-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 8px 12px;
		min-width: 0;
	}

	.session-grid > div {
		min-width: 0;
	}

	.ua {
		grid-column: 1 / -1;
	}

	.label {
		display: block;
		margin-bottom: 3px;
		font-size: 10px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
	}

	code {
		display: block;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-size: 12px;
		color: var(--text-primary);
		background: var(--bg);
		padding: 4px 6px;
		border-radius: 4px;
	}

	.empty-text {
		font-size: 13px;
		color: var(--text-muted);
		padding: 16px 0;
	}

	@media (max-width: 900px) {
		.session-row {
			grid-template-columns: 1fr;
		}

		.session-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
