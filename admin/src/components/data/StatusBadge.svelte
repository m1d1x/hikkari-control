<script lang="ts">
	interface Props {
		status: 'healthy' | 'degraded' | 'down' | 'warning' | 'critical' | 'unknown' | 'running' | 'completed' | 'failed' | 'scheduled';
	}

	let { status }: Props = $props();

	const labels: Record<string, string> = {
		healthy: 'Healthy',
		degraded: 'Degraded',
		down: 'Down',
		warning: 'Warning',
		critical: 'Critical',
		unknown: 'Unknown',
		running: 'Running',
		completed: 'Completed',
		failed: 'Failed',
		scheduled: 'Scheduled',
	};

	const colorMap: Record<string, string> = {
		healthy: 'healthy',
		degraded: 'warning',
		down: 'critical',
		warning: 'warning',
		critical: 'critical',
		unknown: 'unknown',
		running: 'healthy',
		completed: 'healthy',
		failed: 'critical',
		scheduled: 'unknown',
	};
</script>

<span class="badge {colorMap[status]}">
	<span class="dot"></span>
	{labels[status]}
</span>

<style>
	.badge {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		font-weight: 500;
		padding: 3px 10px;
		border-radius: 12px;
	}

	.dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.healthy {
		color: var(--status-healthy);
		background: oklch(0.72 0.15 155 / 0.1);
	}
	.healthy .dot {
		background: var(--status-healthy);
	}

	.warning {
		color: var(--status-warning);
		background: oklch(0.78 0.12 85 / 0.1);
	}
	.warning .dot {
		background: var(--status-warning);
	}

	.critical {
		color: var(--status-critical);
		background: oklch(0.65 0.2 25 / 0.1);
	}
	.critical .dot {
		background: var(--status-critical);
	}

	.unknown {
		color: var(--status-unknown);
		background: oklch(0.52 0 0 / 0.1);
	}
	.unknown .dot {
		background: var(--status-unknown);
	}
</style>
