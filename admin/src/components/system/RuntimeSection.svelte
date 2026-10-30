<script lang="ts">
	import MetricCard from '$components/data/MetricCard.svelte';
	import type { RuntimeInfo } from '$lib/types/system';

	interface Props {
		runtime: RuntimeInfo;
		goroutineSpark: number[];
		heapSpark: number[];
	}

	let { runtime, goroutineSpark, heapSpark }: Props = $props();

	function goroutineLevel(n: number): string {
		if (n >= 2000) return 'var(--status-critical)';
		if (n >= 500) return 'var(--status-warning)';
		return 'var(--status-healthy)';
	}

	let heapPct = $derived(() => {
		const alloc = parseFloat(runtime.heapAlloc);
		const sys = parseFloat(runtime.heapSys);
		if (!sys) return 0;
		return Math.round((alloc / sys) * 100);
	});
</script>

<div class="section">
	<h3 class="section-title">Go Runtime</h3>
	<div class="runtime-grid">
		<div class="runtime-card" style="border-left: 3px solid {goroutineLevel(runtime.goroutines)}">
			<MetricCard label="Goroutines" value={runtime.goroutines} sparkData={goroutineSpark} />
		</div>
		<div class="runtime-card">
			<MetricCard
				label="Heap ({heapPct()}%)"
				value={parseFloat(runtime.heapAlloc)}
				format="bytes"
				sparkData={heapSpark}
			/>
		</div>
		<div class="runtime-card">
			<MetricCard label="GC Runs" value={runtime.gcRuns} />
		</div>
		<div class="runtime-stat-card">
			<span class="stat-label">Go Version</span>
			<span class="stat-value mono">{runtime.goVersion}</span>
		</div>
		{#if runtime.serverUptimeFormatted}
			<div class="runtime-stat-card">
				<span class="stat-label">Server Uptime</span>
				<span class="stat-value mono">{runtime.serverUptimeFormatted}</span>
			</div>
		{/if}
	</div>
</div>

<style>
	.section {
		margin-bottom: 24px;
	}

	.section-title {
		font-size: 14px;
		font-weight: 600;
		color: var(--text-secondary);
		margin-bottom: 12px;
	}

	.runtime-grid {
		display: grid;
		grid-template-columns: repeat(5, 1fr);
		gap: var(--card-gap);
	}

	.runtime-card {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		overflow: hidden;
	}

	.runtime-stat-card {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		padding: var(--card-padding);
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.runtime-stat-card .stat-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.runtime-stat-card .stat-value {
		font-size: 28px;
		font-weight: 600;
		color: var(--text-primary);
		line-height: 1.2;
	}

	@media (max-width: 1200px) {
		.runtime-grid {
			grid-template-columns: repeat(3, 1fr);
		}
	}

	@media (max-width: 600px) {
		.runtime-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
