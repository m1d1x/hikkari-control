<script lang="ts">
	import type { UptimePoint } from '$lib/types/system';

	interface Props {
		checks: UptimePoint[];
		/** Percentage, or null when nothing measured it. */
		uptime: number | null;
		maxSegments?: number;
	}

	let { checks, uptime, maxSegments = 90 }: Props = $props();

	// Downsample checks to maxSegments for display
	let segments = $derived.by(() => {
		if (checks.length <= maxSegments) return checks;
		const step = checks.length / maxSegments;
		const result: UptimePoint[] = [];
		for (let i = 0; i < maxSegments; i++) {
			const idx = Math.floor(i * step);
			result.push(checks[idx]);
		}
		return result;
	});

	function statusColor(status: string): string {
		if (status === 'ok') return 'var(--status-healthy)';
		if (status === 'degraded') return 'var(--status-warning)';
		return 'var(--status-critical)';
	}

	function formatTooltip(point: UptimePoint, idx: number): string {
		const fmt = (d: Date) =>
			d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
		const start = fmt(new Date(point.ts));

		// Show range if we have adjacent segments
		const next = segments[idx + 1];
		const range = next ? `${start}\u2013${fmt(new Date(next.ts))}` : start;

		const latencyMs = Math.round(point.latency * 1000 * 100) / 100;
		return `${range} \u2014 ${point.status === 'ok' ? 'OK' : 'ERROR'} (${latencyMs}ms)`;
	}
</script>

<div class="uptime-bar-wrap">
	<div class="uptime-bar">
		{#each segments as point, idx}
			<div
				class="segment"
				style="background: {statusColor(point.status)}"
				title={formatTooltip(point, idx)}
			></div>
		{/each}
	</div>
	<span class="uptime-pct mono">{uptime === null ? 'нет данных' : `${uptime}%`}</span>
</div>

<style>
	.uptime-bar-wrap {
		display: flex;
		align-items: center;
		gap: 12px;
		flex: 1;
		min-width: 0;
	}

	.uptime-bar {
		display: flex;
		gap: 1px;
		flex: 1;
		height: 20px;
		border-radius: 3px;
		overflow: hidden;
	}

	.segment {
		flex: 1;
		min-width: 1px;
		transition: opacity var(--transition-fast);
		border-radius: 1px;
	}

	.segment:hover {
		opacity: 0.7;
	}

	.uptime-pct {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
		white-space: nowrap;
		min-width: 52px;
		text-align: right;
	}
</style>
