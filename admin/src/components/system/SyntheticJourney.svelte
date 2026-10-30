<script lang="ts">
	import type { SyntheticStep } from '$lib/types/system';
	import SparkLine from '$components/charts/SparkLine.svelte';

	interface Props {
		steps: SyntheticStep[];
	}

	let { steps }: Props = $props();

	// Hide the section entirely when there's no journey data at all (e.g.
	// admin-server has no PROMETHEUS_URL, or Prometheus hasn't seen a single
	// scrape yet). The OpsAlertsBanner already covers actual outages — empty
	// cards would be visual noise.
	let visible = $derived(steps.some((s) => s.hasData));

	const stepLabels: Record<string, string> = {
		login: 'Login',
		refresh: 'Refresh',
		send: 'Send DM',
		read: 'Read DM',
		sfu: 'SFU connect',
	};

	const stepDescriptions: Record<string, string> = {
		login: 'Full login flow with Ed25519 device binding',
		refresh: 'Refresh token rotation (stage1 nonce + stage2 signature)',
		send: 'POST DM with unique marker',
		read: 'GET DM list, verify marker echoed',
		sfu: 'Voice room signaling (planned)',
	};

	function statusClass(s: SyntheticStep): string {
		if (!s.hasData) return 'pending';
		return s.up ? 'ok' : 'down';
	}

	function statusLabel(s: SyntheticStep): string {
		if (!s.hasData) return '—';
		return s.up ? 'OK' : 'DOWN';
	}

	function fmtLatency(ms: number): string {
		if (ms <= 0) return '—';
		if (ms < 1) return '<1ms';
		if (ms < 1000) return `${Math.round(ms)}ms`;
		return `${(ms / 1000).toFixed(2)}s`;
	}

	// Sparkline colour matches the traffic-light state of the latest sample.
	// We pick the colour from the rightmost value in series (= most recent),
	// not from step.up — they're set off the same Prom data but the parent
	// may have a stale instant snapshot relative to the range query.
	function sparkColor(s: SyntheticStep): string {
		const series = s.series ?? [];
		const last = series.length > 0 ? series[series.length - 1] : s.up ? 1 : 0;
		if (last >= 0.5) return 'oklch(0.65 0.15 145)'; // ok green
		return 'oklch(0.65 0.2 25)'; // down red
	}
</script>

{#if visible}
	<div class="section">
		<div class="header">
			<h3>User Journey</h3>
			<span class="subtitle"> Synthetic loop running every 30s from adminru — login → DM → read echo </span>
		</div>
		<div class="grid">
			{#each steps as step (step.step)}
				<div class="card status-{statusClass(step)}">
					<div class="card-head">
						<span class="step-name">{stepLabels[step.step] ?? step.step}</span>
						<span class="status-dot"></span>
					</div>
					<div class="status-text">{statusLabel(step)}</div>
					<div class="latency mono">{fmtLatency(step.latencyMs)}</div>
					<div class="spark">
						<SparkLine data={step.series ?? []} color={sparkColor(step)} height="24px" width="100%" />
					</div>
					<div class="desc">{stepDescriptions[step.step] ?? ''}</div>
				</div>
			{/each}
		</div>
	</div>
{/if}

<style>
	.section {
		margin-bottom: var(--card-gap);
	}

	.header {
		margin-bottom: 12px;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	h3 {
		font-size: 14px;
		font-weight: 600;
		color: var(--text-primary);
		margin: 0;
	}

	.subtitle {
		font-size: 11px;
		color: var(--text-muted);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
		gap: 10px;
	}

	.card {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		padding: 12px 14px;
		display: flex;
		flex-direction: column;
		gap: 4px;
		transition: border-color var(--transition-fast);
	}

	.card.status-ok {
		border-color: oklch(0.65 0.15 145 / 0.4);
	}

	.card.status-down {
		border-color: oklch(0.65 0.2 25 / 0.55);
		box-shadow: 0 0 0 1px oklch(0.65 0.2 25 / 0.2) inset;
	}

	.card.status-pending {
		opacity: 0.6;
	}

	.card-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
	}

	.step-name {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.status-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.status-ok .status-dot {
		background: oklch(0.65 0.15 145);
		box-shadow: 0 0 0 0 oklch(0.65 0.15 145);
	}

	.status-down .status-dot {
		background: var(--status-critical);
		animation: pulse-down 1.6s ease-out infinite;
	}

	.status-pending .status-dot {
		background: var(--text-muted);
	}

	@keyframes pulse-down {
		0% {
			box-shadow: 0 0 0 0 var(--status-critical);
		}
		100% {
			box-shadow: 0 0 0 8px transparent;
		}
	}

	.status-text {
		font-size: 11px;
		font-weight: 700;
		letter-spacing: 0.04em;
	}

	.status-ok .status-text {
		color: oklch(0.65 0.15 145);
	}

	.status-down .status-text {
		color: var(--status-critical);
	}

	.status-pending .status-text {
		color: var(--text-muted);
	}

	.latency {
		font-size: 12px;
		color: var(--text-secondary);
	}

	.spark {
		margin-top: 4px;
		width: 100%;
		opacity: 0.85;
	}

	.desc {
		font-size: 10px;
		color: var(--text-muted);
		line-height: 1.3;
		margin-top: 2px;
	}
</style>
