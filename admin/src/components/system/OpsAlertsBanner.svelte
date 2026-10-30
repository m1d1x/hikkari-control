<script lang="ts">
	import { formatRelativeTime } from '$lib/utils/format';
	import type { ActiveOpsAlert } from '$lib/types/system';

	interface Props {
		alerts: ActiveOpsAlert[];
	}

	let { alerts }: Props = $props();

	const severityOrder: Record<string, number> = { critical: 0, warning: 1, info: 2 };

	let sorted = $derived(
		[...alerts].sort(
			(a, b) =>
				(severityOrder[a.severity] ?? 3) - (severityOrder[b.severity] ?? 3) ||
				a.firstSeen.localeCompare(b.firstSeen),
		),
	);

	let topSeverity = $derived(sorted[0]?.severity ?? 'info');

	function kindLabel(a: ActiveOpsAlert): string {
		switch (a.kind) {
			case 'service_down':
				return 'INTERNAL DOWN';
			case 'target_down':
				return 'SCRAPE DOWN';
			case 'external_down':
				return 'EXTERNAL DOWN';
			case '429_spike':
				return '429 SPIKE';
			default:
				return a.kind.replace(/_/g, ' ').toUpperCase();
		}
	}
</script>

{#if sorted.length > 0}
	<div class="banner sev-{topSeverity}">
		<div class="banner-head">
			<span class="pulse"></span>
			<span class="banner-title">
				{sorted.length} active alert{sorted.length === 1 ? '' : 's'} — same signal as Telegram
			</span>
		</div>
		<div class="alert-list">
			{#each sorted as alert (alert.source + ':' + alert.kind + ':' + alert.key)}
				<div class="alert-item sev-{alert.severity}">
					<span class="kind mono">{kindLabel(alert)}</span>
					<span class="key mono">{alert.key || '—'}</span>
					<span class="message">{alert.message}</span>
					<span class="age mono">{formatRelativeTime(alert.firstSeen)}</span>
				</div>
			{/each}
		</div>
	</div>
{/if}

<style>
	.banner {
		border-radius: var(--card-radius);
		padding: 14px 16px;
		margin-bottom: 16px;
		border: 1px solid;
	}

	.banner.sev-critical {
		background: oklch(0.65 0.2 25 / 0.08);
		border-color: oklch(0.65 0.2 25 / 0.4);
	}

	.banner.sev-warning {
		background: oklch(0.78 0.12 85 / 0.08);
		border-color: oklch(0.78 0.12 85 / 0.4);
	}

	.banner.sev-info {
		background: oklch(0.55 0.05 240 / 0.08);
		border-color: oklch(0.55 0.05 240 / 0.4);
	}

	.banner-head {
		display: flex;
		align-items: center;
		gap: 10px;
		margin-bottom: 10px;
	}

	.pulse {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--status-critical);
		box-shadow: 0 0 0 0 var(--status-critical);
		animation: pulse 1.6s ease-out infinite;
	}

	.banner.sev-warning .pulse {
		background: var(--status-warning);
		box-shadow: 0 0 0 0 var(--status-warning);
	}

	.banner.sev-info .pulse {
		background: var(--status-unknown);
		box-shadow: 0 0 0 0 var(--status-unknown);
		animation: none;
	}

	@keyframes pulse {
		0% {
			box-shadow: 0 0 0 0 currentColor;
		}
		100% {
			box-shadow: 0 0 0 8px transparent;
		}
	}

	.banner-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.alert-list {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.alert-item {
		display: grid;
		grid-template-columns: 130px minmax(120px, max-content) 1fr auto;
		gap: 12px;
		align-items: center;
		padding: 8px 10px;
		background: var(--surface);
		border-radius: 6px;
		font-size: 12px;
	}

	.alert-item .kind {
		font-size: 10px;
		font-weight: 700;
		letter-spacing: 0.04em;
		padding: 2px 7px;
		border-radius: 4px;
		text-align: center;
	}

	.alert-item.sev-critical .kind {
		color: #fff;
		background: var(--status-critical);
	}

	.alert-item.sev-warning .kind {
		color: #000;
		background: var(--status-warning);
	}

	.alert-item.sev-info .kind {
		color: var(--text-primary);
		background: var(--bg-raised);
	}

	.alert-item .key {
		font-size: 12px;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.alert-item .message {
		color: var(--text-secondary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.alert-item .age {
		font-size: 11px;
		color: var(--text-muted);
		white-space: nowrap;
	}

	@media (max-width: 700px) {
		.alert-item {
			grid-template-columns: 1fr;
			gap: 4px;
		}
	}
</style>
