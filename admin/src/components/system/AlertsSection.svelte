<script lang="ts">
	import { formatRelativeTime } from '$lib/utils/format';
	import { acknowledgeAlert } from '$lib/api/system';
	import type { DataQualityAlert } from '$lib/types/system';

	interface Props {
		alerts: DataQualityAlert[];
	}

	let { alerts }: Props = $props();

	let tab: 'active' | 'acknowledged' = $state('active');

	async function handleAck(alert: DataQualityAlert) {
		if (alert.acknowledged) return;
		await acknowledgeAlert(alert.id);
		alert.acknowledged = true;
	}

	const severityOrder = { critical: 0, warning: 1, info: 2 };

	let active = $derived(
		alerts
			.filter((a) => !a.acknowledged)
			.sort((a, b) => severityOrder[a.severity] - severityOrder[b.severity]),
	);

	let acknowledged = $derived(
		alerts
			.filter((a) => a.acknowledged)
			.sort((a, b) => severityOrder[a.severity] - severityOrder[b.severity]),
	);

	let visible = $derived(tab === 'active' ? active : acknowledged);
</script>

{#if alerts.length > 0}
	<div class="section">
		<div class="section-header">
			<h3 class="section-title">Alerts</h3>
			<div class="tabs">
				<button
					class="tab"
					class:active={tab === 'active'}
					onclick={() => (tab = 'active')}
				>
					Active {#if active.length > 0}<span class="count">{active.length}</span>{/if}
				</button>
				<button
					class="tab"
					class:active={tab === 'acknowledged'}
					onclick={() => (tab = 'acknowledged')}
				>
					Acknowledged {#if acknowledged.length > 0}<span class="count dim">{acknowledged.length}</span>{/if}
				</button>
			</div>
		</div>

		{#if visible.length === 0}
			<div class="empty">
				{tab === 'active' ? 'No active alerts' : 'No acknowledged alerts'}
			</div>
		{:else}
			<div class="alerts-list">
				{#each visible as alert (alert.id)}
					<div
						class="alert-item {alert.severity}"
						class:acknowledged={alert.acknowledged}
					>
						<div class="alert-dot"></div>
						<div class="alert-content">
							<span class="alert-message">{alert.message}</span>
							<span class="alert-meta">
								{alert.source} — {formatRelativeTime(alert.timestamp)}
							</span>
						</div>
						{#if !alert.acknowledged}
							<button class="ack-btn" onclick={() => handleAck(alert)}>
								Acknowledge
							</button>
						{/if}
					</div>
				{/each}
			</div>
		{/if}
	</div>
{/if}

<style>
	.section {
		margin-bottom: 24px;
	}

	.section-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 12px;
	}

	.section-title {
		font-size: 14px;
		font-weight: 600;
		color: var(--text-secondary);
	}

	.tabs {
		display: flex;
		gap: 2px;
		background: var(--bg-raised);
		border-radius: 6px;
		padding: 2px;
	}

	.tab {
		font-size: 12px;
		font-weight: 500;
		color: var(--text-muted);
		padding: 4px 12px;
		border-radius: 4px;
		transition: all var(--transition-fast);
		white-space: nowrap;
	}

	.tab:hover {
		color: var(--text-secondary);
	}

	.tab.active {
		background: var(--surface);
		color: var(--text-primary);
	}

	.count {
		font-size: 10px;
		font-weight: 600;
		background: var(--status-critical);
		color: #fff;
		padding: 0 5px;
		border-radius: 8px;
		margin-left: 4px;
	}

	.count.dim {
		background: var(--text-muted);
	}

	.empty {
		font-size: 13px;
		color: var(--text-muted);
		padding: 20px;
		text-align: center;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
	}

	.alerts-list {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		overflow: hidden;
	}

	.alert-item {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 12px 16px;
		border-bottom: 1px solid var(--border-subtle);
	}

	.alert-item:last-child {
		border-bottom: none;
	}

	.alert-item.acknowledged {
		opacity: 0.6;
	}

	.alert-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.alert-item.critical .alert-dot {
		background: var(--status-critical);
	}
	.alert-item.warning .alert-dot {
		background: var(--status-warning);
	}
	.alert-item.info .alert-dot {
		background: var(--status-unknown);
	}

	.alert-content {
		flex: 1;
		min-width: 0;
	}

	.alert-message {
		font-size: 13px;
		color: var(--text-primary);
		display: block;
	}

	.alert-meta {
		font-size: 11px;
		color: var(--text-muted);
		margin-top: 2px;
		display: block;
	}

	.ack-btn {
		font-size: 11px;
		font-weight: 500;
		color: var(--text-secondary);
		padding: 4px 10px;
		border: 1px solid var(--border);
		border-radius: 4px;
		white-space: nowrap;
		transition: all var(--transition-fast);
	}

	.ack-btn:hover {
		color: var(--text-primary);
		border-color: var(--text-muted);
	}
</style>
