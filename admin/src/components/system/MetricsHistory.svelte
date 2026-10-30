<script lang="ts">
	import LineChart from '$components/charts/LineChart.svelte';
	import { getMetricsHistory } from '$lib/api/system';

	let expanded = $state(false);
	let loading = $state(false);
	let selectedQuery = $state('http_rps');
	let selectedRange = $state('24h');
	let labels: string[] = $state([]);
	let values: number[] = $state([]);

	const queries = [
		{ key: 'http_rps', label: 'HTTP RPS' },
		{ key: 'service_latency', label: 'Service Latency' },
		{ key: 'ws_connections', label: 'WS Connections' },
		{ key: 'sfu_rooms', label: 'SFU Voice Rooms' },
		{ key: 'sfu_peers', label: 'SFU Peers (in voice)' },
		{ key: 'sfu_packets_audio', label: 'SFU Audio packets/sec' },
		{ key: 'sfu_packets_video', label: 'SFU Screen-video packets/sec' },
		{ key: 'sfu_packets_screen', label: 'SFU Screen-audio packets/sec' },
		{ key: 'redis_memory', label: 'Redis Memory' },
		{ key: 'redis_ops', label: 'Redis Ops/sec' },
	];

	const ranges = ['1h', '6h', '24h', '7d', '30d'];

	async function load() {
		loading = true;
		const points = await getMetricsHistory(selectedQuery, selectedRange);
		labels = points.map((p) => {
			const d = new Date(p.ts * 1000);
			return d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
		});
		values = points.map((p) => p.value);
		loading = false;
	}

	function toggle() {
		expanded = !expanded;
		if (expanded && values.length === 0) load();
	}

	function onQueryChange() {
		load();
	}

	function onRangeChange() {
		load();
	}
</script>

<div class="section">
	<button class="toggle" onclick={toggle}>
		<span class="chevron" class:open={expanded}></span>
		<h3 class="section-title">Historical Metrics (Prometheus)</h3>
	</button>

	{#if expanded}
		<div class="panel">
			<div class="toolbar">
				<div class="selectors">
					<select bind:value={selectedQuery} onchange={onQueryChange}>
						{#each queries as q}
							<option value={q.key}>{q.label}</option>
						{/each}
					</select>
					<div class="range-group">
						{#each ranges as r}
							<button
								class="range-btn"
								class:active={selectedRange === r}
								onclick={() => { selectedRange = r; onRangeChange(); }}
							>
								{r}
							</button>
						{/each}
					</div>
				</div>
			</div>

			{#if loading}
				<div class="loading">Loading...</div>
			{:else if values.length === 0}
				<div class="loading">No data available</div>
			{:else}
				<LineChart
					{labels}
					series={[{ name: queries.find((q) => q.key === selectedQuery)?.label ?? '', data: values }]}
					height="280px"
					showLegend={false}
				/>
			{/if}
		</div>
	{/if}
</div>

<style>
	.section {
		margin-bottom: 24px;
	}

	.toggle {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		text-align: left;
		padding: 0;
		color: var(--text-secondary);
	}

	.toggle:hover {
		color: var(--text-primary);
	}

	.section-title {
		font-size: 14px;
		font-weight: 600;
		margin: 0;
	}

	.chevron {
		width: 0;
		height: 0;
		border-left: 5px solid currentColor;
		border-top: 4px solid transparent;
		border-bottom: 4px solid transparent;
		transition: transform var(--transition-fast);
	}

	.chevron.open {
		transform: rotate(90deg);
	}

	.panel {
		margin-top: 12px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: var(--card-radius);
		padding: var(--card-padding);
	}

	.toolbar {
		margin-bottom: 16px;
	}

	.selectors {
		display: flex;
		align-items: center;
		gap: 12px;
	}

	select {
		font-size: 12px;
		padding: 4px 8px;
		border: 1px solid var(--border);
		border-radius: 4px;
		background: var(--bg-raised);
		color: var(--text-primary);
	}

	.range-group {
		display: flex;
		gap: 2px;
	}

	.range-btn {
		font-size: 11px;
		padding: 3px 8px;
		border-radius: 4px;
		color: var(--text-muted);
		font-weight: 500;
		transition: all var(--transition-fast);
	}

	.range-btn:hover {
		color: var(--text-secondary);
		background: var(--surface-hover);
	}

	.range-btn.active {
		color: var(--text-primary);
		background: var(--surface-active);
	}

	.loading {
		text-align: center;
		padding: 40px;
		color: var(--text-muted);
		font-size: 13px;
	}
</style>
