<script lang="ts">
	import type { AdminLogEntry } from '$lib/api/logs';
	import type { Group } from '$lib/logs/group';
	import LogRow from './LogRow.svelte';

	interface Props {
		group: Group;
		expanded: boolean;
		onToggle: () => void;
	}

	let { group, expanded, onToggle }: Props = $props();
	let expandedEntry = $state<string | null>(null);

	function entryKey(e: AdminLogEntry, i: number): string {
		return `${e.ts}-${i}-${e.msg.slice(0, 16)}`;
	}

	function toggleEntry(k: string) {
		expandedEntry = expandedEntry === k ? null : k;
	}

	function fmtLatest(ts: string): string {
		const d = new Date(ts);
		const hh = String(d.getHours()).padStart(2, '0');
		const mm = String(d.getMinutes()).padStart(2, '0');
		const ss = String(d.getSeconds()).padStart(2, '0');
		return `${hh}:${mm}:${ss}`;
	}

	function preview(msg: string): string {
		const normalized = msg.replace(/\s+/g, ' ').trim();
		return normalized.length > 120 ? normalized.slice(0, 120) + '…' : normalized;
	}

	function openAllHref(): string {
		const q = group.samplemsg.slice(0, 40);
		const params = new URLSearchParams({ q });
		return `/logs?${params.toString()}`;
	}
</script>

<div
	class="group-row"
	class:expanded
	onclick={onToggle}
	onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && onToggle()}
	role="button"
	tabindex="0"
>
	<span class="chev">{expanded ? '▾' : '▸'}</span>
	<span class="count">×{group.count}</span>
	<span class="cat" title={group.cat}>{group.cat || '—'}</span>
	<span class="msg" title={group.samplemsg}>{preview(group.samplemsg)}</span>
	<span class="latest">{fmtLatest(group.latestTs)}</span>
</div>

{#if expanded}
	<div class="group-body">
		{#each group.entries as e, i (entryKey(e, i))}
			{@const k = entryKey(e, i)}
			<LogRow log={e} expanded={expandedEntry === k} onToggle={() => toggleEntry(k)} />
		{/each}
		<div class="group-actions">
			<a class="show-all" href={openAllHref()} target="_blank" rel="noopener noreferrer">
				Показать все похожие →
			</a>
		</div>
	</div>
{/if}

<style>
	.group-row {
		display: grid;
		grid-template-columns: 20px 48px 96px 1fr 72px;
		gap: 8px;
		padding: 6px 12px;
		align-items: center;
		cursor: pointer;
		font-size: 12px;
		border-bottom: 1px solid var(--border-subtle);
		transition: background var(--transition-fast);
	}
	.group-row:hover {
		background: oklch(0.15 0 0);
	}
	.group-row.expanded {
		background: oklch(0.17 0 0);
	}
	.chev {
		color: var(--text-muted);
		font-size: 10px;
	}
	.count {
		font-family: var(--font-mono);
		color: var(--status-critical);
		font-weight: 600;
	}
	.cat {
		color: var(--chart-4, var(--chart-2));
		text-transform: lowercase;
		font-size: 11px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.msg {
		color: var(--text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.latest {
		color: var(--text-muted);
		font-family: var(--font-mono);
		font-size: 11px;
		text-align: right;
	}
	.group-body {
		background: oklch(0.09 0 0);
		border-bottom: 1px solid var(--border-subtle);
		padding: 4px 0;
	}
	.group-actions {
		display: flex;
		justify-content: flex-end;
		padding: 6px 12px;
	}
	.show-all {
		font-size: 11px;
		color: var(--chart-2);
		text-decoration: none;
		font-family: inherit;
	}
	.show-all:hover {
		text-decoration: underline;
	}
</style>
