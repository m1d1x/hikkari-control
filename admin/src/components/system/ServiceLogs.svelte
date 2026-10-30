<script lang="ts">
	import type { LogEntry } from '$lib/types/system';
	import { getServiceLogs } from '$lib/api/system';

	interface Props {
		service: string;
	}

	let { service }: Props = $props();

	let entries: LogEntry[] = $state([]);
	let loading = $state(false);
	let loaded = $state(false);
	let expanded = $state(false);
	let filter: 'all' | 'info' | 'warn' | 'error' = $state('all');

	let filtered = $derived(
		filter === 'all' ? entries : entries.filter((e) => e.level === filter),
	);

	async function load() {
		if (loaded) return;
		loading = true;
		const result = await getServiceLogs(service);
		entries = result.entries || [];
		loading = false;
		loaded = true;
	}

	function toggle() {
		expanded = !expanded;
		if (expanded && !loaded) load();
	}

	async function refresh() {
		loaded = false;
		await load();
	}

	function levelColor(level: string): string {
		if (level === 'error') return 'var(--status-critical)';
		if (level === 'warn') return 'var(--status-warning)';
		return 'var(--text-muted)';
	}

	function formatTime(ts: string): string {
		return new Date(ts).toLocaleTimeString('ru-RU', {
			hour: '2-digit',
			minute: '2-digit',
			second: '2-digit',
		});
	}

	let errorCount = $derived(entries.filter((e) => e.level === 'error').length);
	let warnCount = $derived(entries.filter((e) => e.level === 'warn').length);
</script>

<div class="logs-section">
	<button class="logs-toggle" onclick={toggle}>
		<span class="chevron" class:open={expanded}></span>
		<span class="logs-label">Logs</span>
		{#if loaded && errorCount > 0}
			<span class="log-count error">{errorCount} err</span>
		{/if}
		{#if loaded && warnCount > 0}
			<span class="log-count warn">{warnCount} warn</span>
		{/if}
	</button>

	{#if expanded}
		<div class="logs-panel">
			<div class="logs-toolbar">
				<div class="filter-group">
					{#each ['all', 'error', 'warn', 'info'] as lvl}
						<button
							class="filter-btn"
							class:active={filter === lvl}
							onclick={() => (filter = lvl as typeof filter)}
						>
							{lvl}
						</button>
					{/each}
				</div>
				<button class="refresh-btn" onclick={refresh} disabled={loading}>
					{loading ? '...' : 'Refresh'}
				</button>
			</div>

			<div class="logs-list">
				{#if loading}
					<div class="log-empty">Loading...</div>
				{:else if filtered.length === 0}
					<div class="log-empty">No logs</div>
				{:else}
					{#each filtered as entry}
						<div class="log-entry">
							<span class="log-time mono">{formatTime(entry.ts)}</span>
							<span class="log-level mono" style="color: {levelColor(entry.level)}">{entry.level.toUpperCase().padEnd(5)}</span>
							<span class="log-msg mono">{entry.message}</span>
						</div>
					{/each}
				{/if}
			</div>
		</div>
	{/if}
</div>

<style>
	.logs-section {
		border-top: 1px solid var(--border-subtle);
		margin-top: 12px;
		padding-top: 8px;
	}

	.logs-toggle {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		color: var(--text-muted);
		padding: 4px 0;
		width: 100%;
		text-align: left;
		transition: color var(--transition-fast);
	}

	.logs-toggle:hover {
		color: var(--text-secondary);
	}

	.chevron {
		width: 0;
		height: 0;
		border-left: 4px solid currentColor;
		border-top: 3px solid transparent;
		border-bottom: 3px solid transparent;
		transition: transform var(--transition-fast);
	}

	.chevron.open {
		transform: rotate(90deg);
	}

	.logs-label {
		font-weight: 500;
	}

	.log-count {
		font-size: 10px;
		padding: 1px 6px;
		border-radius: 8px;
		font-weight: 600;
	}

	.log-count.error {
		color: var(--status-critical);
		background: oklch(0.65 0.2 25 / 0.12);
	}

	.log-count.warn {
		color: var(--status-warning);
		background: oklch(0.78 0.12 85 / 0.12);
	}

	.logs-panel {
		margin-top: 8px;
	}

	.logs-toolbar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 8px;
	}

	.filter-group {
		display: flex;
		gap: 2px;
	}

	.filter-btn {
		font-size: 11px;
		padding: 3px 8px;
		border-radius: 4px;
		color: var(--text-muted);
		font-weight: 500;
		transition: all var(--transition-fast);
	}

	.filter-btn:hover {
		color: var(--text-secondary);
		background: var(--surface-hover);
	}

	.filter-btn.active {
		color: var(--text-primary);
		background: var(--surface-active);
	}

	.refresh-btn {
		font-size: 11px;
		padding: 3px 10px;
		border: 1px solid var(--border);
		border-radius: 4px;
		color: var(--text-secondary);
		transition: all var(--transition-fast);
	}

	.refresh-btn:hover:not(:disabled) {
		color: var(--text-primary);
		border-color: var(--text-muted);
	}

	.refresh-btn:disabled {
		opacity: 0.5;
	}

	.logs-list {
		background: oklch(0.12 0 0);
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
		max-height: 240px;
		overflow-y: auto;
		padding: 4px 0;
	}

	.log-entry {
		display: flex;
		gap: 10px;
		padding: 3px 12px;
		font-size: 11.5px;
		line-height: 1.6;
	}

	.log-entry:hover {
		background: oklch(0.14 0 0);
	}

	.log-time {
		color: var(--text-muted);
		white-space: nowrap;
		flex-shrink: 0;
	}

	.log-level {
		flex-shrink: 0;
		font-weight: 600;
		width: 44px;
	}

	.log-msg {
		color: var(--text-secondary);
		word-break: break-all;
	}

	.log-empty {
		padding: 16px;
		text-align: center;
		color: var(--text-muted);
		font-size: 12px;
	}
</style>
