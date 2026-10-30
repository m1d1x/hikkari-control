<script lang="ts">
	import { formatRelativeTime } from '$lib/utils/format';
	import { acknowledgeIncident } from '$lib/api/system';
	import type { Incident } from '$lib/types/system';

	interface Props {
		incidents: Incident[];
	}

	let { incidents }: Props = $props();

	let tab: 'active' | 'acknowledged' = $state('active');

	async function handleAck(incident: Incident) {
		if (incident.acknowledged) return;
		await acknowledgeIncident(incident.id);
		incident.acknowledged = true;
	}

	let active = $derived(incidents.filter((i) => !i.acknowledged));
	let acknowledged = $derived(incidents.filter((i) => i.acknowledged));
	let visible = $derived(tab === 'active' ? active : acknowledged);
</script>

<div class="section">
	<div class="section-header">
		<h3 class="section-title">Incidents</h3>
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
			{tab === 'active' ? 'No active incidents' : 'No acknowledged incidents'}
		</div>
	{:else}
		<div class="timeline">
			{#each visible as incident (incident.id)}
				<div class="incident" class:acked={incident.acknowledged}>
					<div class="dot-col">
						<div
							class="dot"
							class:downtime={incident.type === 'downtime'}
							class:degraded={incident.type === 'degraded'}
						></div>
						<div class="line"></div>
					</div>
					<div class="content">
						<div class="header">
							<span class="service">{incident.service}</span>
							<span class="type-badge" class:downtime={incident.type === 'downtime'}>
								{incident.type}
							</span>
							<span class="duration mono">{incident.duration}</span>
						</div>
						<div class="meta">
							{formatRelativeTime(incident.startedAt)}
							{#if incident.resolvedAt}
								— resolved
							{:else}
								— ongoing
							{/if}
						</div>
					</div>
					{#if !incident.acknowledged}
						<button class="ack-btn" onclick={() => handleAck(incident)}>
							Acknowledge
						</button>
					{/if}
				</div>
			{/each}
		</div>
	{/if}
</div>

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

	.timeline {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		padding: 12px 16px;
	}

	.incident {
		display: flex;
		gap: 12px;
		padding: 8px 0;
		align-items: flex-start;
	}

	.incident.acked {
		opacity: 0.6;
	}

	.incident:last-child .line {
		display: none;
	}

	.dot-col {
		display: flex;
		flex-direction: column;
		align-items: center;
		padding-top: 4px;
	}

	.dot {
		width: 10px;
		height: 10px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.dot.downtime {
		background: var(--status-critical);
	}

	.dot.degraded {
		background: var(--status-warning);
	}

	.line {
		width: 1px;
		flex: 1;
		background: var(--border-subtle);
		margin-top: 4px;
	}

	.content {
		flex: 1;
		min-width: 0;
	}

	.header {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.service {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
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

	.duration {
		font-size: 12px;
		color: var(--text-muted);
	}

	.meta {
		font-size: 11px;
		color: var(--text-muted);
		margin-top: 2px;
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
		margin-top: 2px;
		transition: all var(--transition-fast);
	}

	.ack-btn:hover {
		color: var(--text-primary);
		border-color: var(--text-muted);
	}
</style>
