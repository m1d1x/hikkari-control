<script lang="ts">
	import { formatRelativeTime } from '$lib/utils/format';
	import { acknowledgeAlert, acknowledgeIncident } from '$lib/api/system';
	import type { DataQualityAlert, Incident } from '$lib/types/system';

	interface Props {
		alerts: DataQualityAlert[];
		incidents: Incident[];
	}

	let { alerts, incidents }: Props = $props();

	type Tab = 'incidents' | 'alerts' | 'acknowledged';
	let tab: Tab = $state('incidents');

	const severityOrder: Record<string, number> = { critical: 0, warning: 1, info: 2 };

	let activeIncidents = $derived(incidents.filter((i) => !i.acknowledged));
	let activeAlerts = $derived(
		alerts
			.filter((a) => !a.acknowledged)
			.sort((a, b) => severityOrder[a.severity] - severityOrder[b.severity]),
	);

	let ackedIncidents = $derived(incidents.filter((i) => i.acknowledged));
	let ackedAlerts = $derived(
		alerts
			.filter((a) => a.acknowledged)
			.sort((a, b) => severityOrder[a.severity] - severityOrder[b.severity]),
	);

	let totalActive = $derived(activeIncidents.length + activeAlerts.length);
	let totalAcked = $derived(ackedIncidents.length + ackedAlerts.length);

	async function ackAlert(alert: DataQualityAlert) {
		if (alert.acknowledged) return;
		await acknowledgeAlert(alert.id);
		alert.acknowledged = true;
	}

	async function ackIncident(incident: Incident) {
		if (incident.acknowledged) return;
		await acknowledgeIncident(incident.id);
		incident.acknowledged = true;
	}
</script>

<div class="section">
	<div class="section-header">
		<div class="tabs">
			<button class="tab" class:active={tab === 'incidents'} onclick={() => (tab = 'incidents')}>
				Incidents
				{#if activeIncidents.length > 0}
					<span class="count">{activeIncidents.length}</span>
				{/if}
			</button>
			<button class="tab" class:active={tab === 'alerts'} onclick={() => (tab = 'alerts')}>
				Alerts
				{#if activeAlerts.length > 0}
					<span class="count">{activeAlerts.length}</span>
				{/if}
			</button>
			<button class="tab" class:active={tab === 'acknowledged'} onclick={() => (tab = 'acknowledged')}>
				Acknowledged
				{#if totalAcked > 0}
					<span class="count dim">{totalAcked}</span>
				{/if}
			</button>
		</div>
	</div>

	{#if tab === 'incidents'}
		{#if activeIncidents.length === 0}
			<div class="empty">No active incidents</div>
		{:else}
			<div class="list">
				{#each activeIncidents as incident (incident.id)}
					<div class="item">
						<div class="dot" class:downtime={incident.type === 'downtime'} class:degraded={incident.type === 'degraded'}></div>
						<div class="item-content">
							<div class="item-header">
								<span class="item-title">{incident.service}</span>
								<span class="type-badge" class:downtime={incident.type === 'downtime'}>{incident.type}</span>
								<span class="mono dim-text">{incident.duration}</span>
							</div>
							<span class="item-meta">
								{formatRelativeTime(incident.startedAt)}
								{#if incident.resolvedAt}— resolved{:else}— ongoing{/if}
							</span>
						</div>
						<button class="ack-btn" onclick={() => ackIncident(incident)}>Ack</button>
					</div>
				{/each}
			</div>
		{/if}
	{:else if tab === 'alerts'}
		{#if activeAlerts.length === 0}
			<div class="empty">No active alerts</div>
		{:else}
			<div class="list">
				{#each activeAlerts as alert (alert.id)}
					<div class="item">
						<div class="dot" class:critical={alert.severity === 'critical'} class:warning={alert.severity === 'warning'} class:info={alert.severity === 'info'}></div>
						<div class="item-content">
							<span class="item-title">{alert.message}</span>
							<span class="item-meta">{alert.source} — {formatRelativeTime(alert.timestamp)}</span>
						</div>
						<button class="ack-btn" onclick={() => ackAlert(alert)}>Ack</button>
					</div>
				{/each}
			</div>
		{/if}
	{:else}
		{#if totalAcked === 0}
			<div class="empty">Nothing acknowledged</div>
		{:else}
			<div class="list">
				{#each ackedIncidents as incident (incident.id)}
					<div class="item acked">
						<div class="dot" class:downtime={incident.type === 'downtime'} class:degraded={incident.type === 'degraded'}></div>
						<div class="item-content">
							<div class="item-header">
								<span class="item-title">{incident.service}</span>
								<span class="type-badge" class:downtime={incident.type === 'downtime'}>{incident.type}</span>
								<span class="mono dim-text">{incident.duration}</span>
							</div>
							<span class="item-meta">
								{formatRelativeTime(incident.startedAt)}
								{#if incident.resolvedAt}— resolved{:else}— ongoing{/if}
							</span>
						</div>
					</div>
				{/each}
				{#each ackedAlerts as alert (alert.id)}
					<div class="item acked">
						<div class="dot" class:critical={alert.severity === 'critical'} class:warning={alert.severity === 'warning'} class:info={alert.severity === 'info'}></div>
						<div class="item-content">
							<span class="item-title">{alert.message}</span>
							<span class="item-meta">{alert.source} — {formatRelativeTime(alert.timestamp)}</span>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	{/if}
</div>

<style>
	.section {
		margin-bottom: 24px;
	}

	.section-header {
		margin-bottom: 12px;
	}

	.tabs {
		display: flex;
		gap: 2px;
		background: var(--bg-raised);
		border-radius: 6px;
		padding: 2px;
		width: fit-content;
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

	.list {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		overflow: hidden;
	}

	.item {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 12px 16px;
		border-bottom: 1px solid var(--border-subtle);
	}

	.item:last-child {
		border-bottom: none;
	}

	.item.acked {
		opacity: 0.5;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.dot.critical,
	.dot.downtime {
		background: var(--status-critical);
	}

	.dot.warning,
	.dot.degraded {
		background: var(--status-warning);
	}

	.dot.info {
		background: var(--status-unknown);
	}

	.item-content {
		flex: 1;
		min-width: 0;
	}

	.item-header {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.item-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
		display: block;
	}

	.item-meta {
		font-size: 11px;
		color: var(--text-muted);
		margin-top: 2px;
		display: block;
	}

	.dim-text {
		font-size: 12px;
		color: var(--text-muted);
	}

	.type-badge {
		font-size: 10px;
		padding: 1px 6px;
		border-radius: 4px;
		font-weight: 500;
		color: var(--status-warning);
		background: oklch(0.78 0.12 85 / 0.12);
	}

	.type-badge.downtime {
		color: var(--status-critical);
		background: oklch(0.65 0.2 25 / 0.12);
	}

	.ack-btn {
		font-size: 11px;
		font-weight: 500;
		color: var(--text-secondary);
		padding: 4px 10px;
		border: 1px solid var(--border);
		border-radius: 4px;
		white-space: nowrap;
		flex-shrink: 0;
		transition: all var(--transition-fast);
	}

	.ack-btn:hover {
		color: var(--text-primary);
		border-color: var(--text-muted);
	}
</style>
