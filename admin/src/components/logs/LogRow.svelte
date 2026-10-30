<script lang="ts">
	import type { AdminLogEntry } from '$lib/api/logs';
	import type { TraceSpan } from '$lib/api/trace';
	import { getCachedTrace, peekCachedTrace } from '$lib/api/traceCache';

	interface Props {
		log: AdminLogEntry;
		expanded: boolean;
		onToggle: () => void;
	}

	let { log, expanded, onToggle }: Props = $props();

	function formatTime(ts: string): string {
		const d = new Date(ts);
		const hh = String(d.getHours()).padStart(2, '0');
		const mm = String(d.getMinutes()).padStart(2, '0');
		const ss = String(d.getSeconds()).padStart(2, '0');
		const ms = String(d.getMilliseconds()).padStart(3, '0');
		return `${hh}:${mm}:${ss}.${ms}`;
	}

	function shortUser(id: string): string {
		if (!id || id === '00000000-0000-0000-0000-000000000000') return '';
		return id.slice(0, 8);
	}

	let isSystem = $derived(
		!log.userId || log.userId === '00000000-0000-0000-0000-000000000000',
	);

	function ctxText(): string {
		if (log.ctx === null || log.ctx === undefined) return '';
		if (typeof log.ctx === 'string') return log.ctx;
		try {
			return JSON.stringify(log.ctx, null, 2);
		} catch {
			return String(log.ctx);
		}
	}

	let hasCtx = $derived(
		log.ctx !== null &&
			log.ctx !== undefined &&
			!(typeof log.ctx === 'string' && log.ctx === '') &&
			!(typeof log.ctx === 'object' && Object.keys(log.ctx as object).length === 0),
	);

	let hasTrace = $derived(!!log.traceId);
	let hasDetails = $derived(hasCtx || hasTrace);

	// Trace data is fetched lazily and shared via module-level cache,
	// so opening many rows for the same trace costs only one request.
	let spans: TraceSpan[] = $state([]);
	let traceLoading = $state(false);
	let traceError: string | null = $state(null);

	$effect(() => {
		if (!hasTrace) return;
		const id = log.traceId!;
		const cached = peekCachedTrace(id);
		if (cached) {
			spans = cached.spans;
			return;
		}
		traceLoading = true;
		traceError = null;
		getCachedTrace(id)
			.then((res) => {
				spans = res.spans;
			})
			.catch((e) => {
				traceError = e instanceof Error ? e.message : String(e);
			})
			.finally(() => {
				traceLoading = false;
			});
	});

	type Row = TraceSpan & {
		depth: number;
		offsetMs: number;
		widthPct: number;
		offsetPct: number;
	};

	let traceTotalMs = $derived.by(() => {
		if (spans.length === 0) return 0;
		const start = spans.reduce(
			(acc, s) => Math.min(acc, new Date(s.startedAt).getTime()),
			Infinity,
		);
		const end = spans.reduce(
			(acc, s) => Math.max(acc, new Date(s.startedAt).getTime() + s.durationMs),
			0,
		);
		return Math.max(0, end - start);
	});

	let waterfallRows: Row[] = $derived.by(() => {
		if (spans.length === 0) return [];
		const sorted = [...spans].sort((a, b) => a.startedAt.localeCompare(b.startedAt));
		const traceStart = new Date(sorted[0].startedAt).getTime();
		const total = Math.max(1, traceTotalMs);
		const childrenOf = new Map<string, TraceSpan[]>();
		const allIds = new Set(sorted.map((s) => s.spanId));
		for (const s of sorted) {
			const parent = allIds.has(s.parentSpanId) ? s.parentSpanId : '';
			if (!childrenOf.has(parent)) childrenOf.set(parent, []);
			childrenOf.get(parent)!.push(s);
		}
		const out: Row[] = [];
		const visit = (parentId: string, depth: number) => {
			const kids = childrenOf.get(parentId) || [];
			for (const k of kids) {
				const offsetMs = new Date(k.startedAt).getTime() - traceStart;
				out.push({
					...k,
					depth,
					offsetMs,
					offsetPct: (offsetMs / total) * 100,
					widthPct: Math.max(0.5, (k.durationMs / total) * 100),
				});
				visit(k.spanId, depth + 1);
			}
		};
		visit('', 0);
		return out;
	});

	function fmtDuration(ms: number): string {
		if (ms < 1) return '<1ms';
		if (ms < 1000) return `${Math.round(ms)}ms`;
		return `${(ms / 1000).toFixed(2)}s`;
	}

	function shortTrace(id: string): string {
		return id.length > 8 ? id.slice(0, 8) : id;
	}
</script>

<div
	class="row level-{log.level}"
	class:expandable={hasDetails}
	class:expanded
	onclick={onToggle}
	onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && onToggle()}
	role="button"
	tabindex="0"
>
	<span class="time">{formatTime(log.ts)}</span>
	<span class="level level-{log.level}">{log.level}</span>
	<span class="source">{log.source}</span>
	<span class="cat">{log.cat || '—'}</span>
	<span class="user" title={log.userId}>
		{#if isSystem}
			<span class="system-badge" title="System-level event, not attributed to a user">system</span>
		{:else}
			{shortUser(log.userId)}
		{/if}
	</span>
	<span class="msg">{log.msg}</span>
	<span class="dur" title={hasTrace ? `Trace duration: ${traceTotalMs}ms` : ''}>
		{#if hasTrace}
			{#if traceLoading && spans.length === 0}
				<span class="dur-loading">…</span>
			{:else if traceError}
				<span class="dur-err" title={traceError}>err</span>
			{:else if traceTotalMs > 0}
				{fmtDuration(traceTotalMs)}
			{/if}
		{/if}
	</span>
	<span class="trace">
		{#if hasTrace}
			<a
				href="/trace/{log.traceId}"
				class="trace-link"
				title="Open trace {log.traceId}"
				onclick={(e) => e.stopPropagation()}
			>→</a>
		{/if}
	</span>
</div>

{#if expanded && hasDetails}
	<div class="ctx-panel">
		{#if hasCtx}
			<pre class="ctx">{ctxText()}</pre>
		{/if}

		{#if hasTrace}
			<div class="waterfall-block">
				<div class="waterfall-header">
					<span class="wf-title">Waterfall</span>
					<span class="wf-meta">
						{waterfallRows.length} spans · {fmtDuration(traceTotalMs)}
					</span>
					<a
						href="/trace/{log.traceId}"
						class="wf-fullscreen"
						onclick={(e) => e.stopPropagation()}
					>full view ↗</a>
				</div>
				{#if traceLoading && waterfallRows.length === 0}
					<div class="wf-empty">Загрузка trace…</div>
				{:else if traceError}
					<div class="wf-empty wf-err">Ошибка: {traceError}</div>
				{:else if waterfallRows.length === 0}
					<div class="wf-empty">Нет spans для trace {shortTrace(log.traceId!)}</div>
				{:else}
					<div class="waterfall">
						{#each waterfallRows as r (r.spanId)}
							<div class="wf-row" title={`${r.name} — ${r.durationMs}ms`}>
								<div class="wf-name" style="padding-left: {r.depth * 12}px">
									<span class="wf-depth">{r.depth > 0 ? '└' : ''}</span>
									<span class="wf-span-name">{r.name}</span>
									<span class="wf-span-dur">{fmtDuration(r.durationMs)}</span>
								</div>
								<div class="wf-track">
									<div
										class="wf-bar"
										class:err={r.status === 'error'}
										style="left: {r.offsetPct}%; width: {r.widthPct}%"
									></div>
								</div>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		{/if}

		<div class="meta">
			{#if log.traceId}
				<span class="meta-item">
					trace:
					<a href="/trace/{log.traceId}" class="mono trace-link-inline">{log.traceId}</a>
				</span>
			{/if}
			{#if log.requestId}
				<span class="meta-item">request: <span class="mono">{log.requestId}</span></span>
			{/if}
			{#if log.sessionId}
				<span class="meta-item">session: <span class="mono">{log.sessionId}</span></span>
			{/if}
			{#if log.userId && log.userId !== '00000000-0000-0000-0000-000000000000'}
				<span class="meta-item">user: <span class="mono">{log.userId}</span></span>
			{/if}
			{#if log.serverId}
				<span class="meta-item">server: <span class="mono">{log.serverId}</span></span>
			{/if}
			<span class="meta-item">ts: <span class="mono">{log.ts}</span></span>
		</div>
	</div>
{/if}

<style>
	.row {
		display: grid;
		grid-template-columns: 92px 52px 72px 90px 80px 1fr 56px 28px;
		gap: 8px;
		padding: 3px 12px;
		line-height: 1.6;
		font-size: 12px;
		transition: background var(--transition-fast);
		cursor: default;
	}
	.row.expandable {
		cursor: pointer;
	}
	.row:hover {
		background: oklch(0.15 0 0);
	}
	.row.expanded {
		background: oklch(0.17 0 0);
	}
	.row.level-error {
		background: oklch(0.15 0.04 25 / 0.3);
	}
	.row.level-warn {
		background: oklch(0.15 0.03 85 / 0.2);
	}
	.time {
		color: var(--text-muted);
	}
	.level {
		font-weight: 600;
		text-transform: uppercase;
		font-size: 11px;
	}
	.level.level-error {
		color: var(--status-critical);
	}
	.level.level-warn {
		color: var(--status-warning);
	}
	.level.level-info {
		color: var(--chart-2);
	}
	.level.level-debug,
	.level.level-trace {
		color: var(--text-muted);
	}
	.source {
		color: var(--chart-3);
	}
	.cat {
		color: var(--chart-4, var(--chart-2));
	}
	.user {
		color: var(--text-muted);
	}
	.system-badge {
		display: inline-block;
		padding: 1px 6px;
		border-radius: 3px;
		background: oklch(0.2 0.04 240);
		color: var(--chart-2);
		font-size: 10px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.3px;
	}
	.msg {
		color: var(--text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.row.expanded .msg {
		white-space: normal;
		overflow: visible;
		word-break: break-all;
	}
	.dur {
		color: var(--text-muted);
		font-size: 11px;
		text-align: right;
		font-family: var(--font-mono);
	}
	.dur-loading {
		opacity: 0.5;
	}
	.dur-err {
		color: var(--status-critical);
	}
	.ctx-panel {
		padding: 8px 12px 12px 104px;
		background: oklch(0.12 0 0);
		border-bottom: 1px solid var(--border-subtle);
	}
	.ctx {
		background: oklch(0.08 0 0);
		padding: 8px 10px;
		border-radius: 4px;
		font-size: 11px;
		color: var(--text-primary);
		white-space: pre-wrap;
		word-break: break-all;
		margin: 0 0 6px 0;
		font-family: var(--font-mono);
	}
	.meta {
		display: flex;
		flex-wrap: wrap;
		gap: 12px;
		font-size: 10px;
		color: var(--text-muted);
	}
	.mono {
		font-family: var(--font-mono);
		color: var(--text-secondary);
	}
	.trace {
		display: flex;
		align-items: center;
		justify-content: center;
	}
	.trace-link {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 22px;
		height: 22px;
		border-radius: 4px;
		color: var(--chart-2);
		text-decoration: none;
		font-size: 14px;
		line-height: 1;
		transition: background var(--transition-fast);
	}
	.trace-link:hover {
		background: oklch(0.22 0.04 240);
		color: var(--text-primary);
	}
	.trace-link-inline {
		color: var(--chart-2);
		text-decoration: none;
	}
	.trace-link-inline:hover {
		text-decoration: underline;
	}

	.waterfall-block {
		margin: 0 0 8px 0;
		border: 1px solid var(--border-subtle);
		border-radius: 4px;
		background: oklch(0.08 0 0);
	}
	.waterfall-header {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 6px 10px;
		border-bottom: 1px solid var(--border-subtle);
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}
	.wf-title {
		color: var(--text-secondary);
		font-weight: 600;
	}
	.wf-meta {
		font-family: var(--font-mono);
		text-transform: none;
		letter-spacing: 0;
		font-size: 11px;
	}
	.wf-fullscreen {
		margin-left: auto;
		color: var(--chart-2);
		text-decoration: none;
		font-size: 11px;
		text-transform: none;
	}
	.wf-fullscreen:hover {
		text-decoration: underline;
	}
	.wf-empty {
		padding: 12px;
		text-align: center;
		color: var(--text-muted);
		font-size: 11px;
	}
	.wf-empty.wf-err {
		color: var(--status-critical);
	}
	.waterfall {
		font-family: var(--font-mono);
		font-size: 11px;
		max-height: 280px;
		overflow-y: auto;
	}
	.wf-row {
		display: grid;
		grid-template-columns: minmax(220px, 38%) 1fr;
		align-items: center;
		gap: 10px;
		padding: 3px 10px;
		border-bottom: 1px solid oklch(0.13 0 0);
	}
	.wf-row:last-child {
		border-bottom: none;
	}
	.wf-name {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		color: var(--text-primary);
	}
	.wf-depth {
		color: var(--text-muted);
	}
	.wf-span-name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		flex: 1;
	}
	.wf-span-dur {
		color: var(--text-muted);
		font-size: 10px;
	}
	.wf-track {
		position: relative;
		height: 10px;
		background: oklch(0.13 0 0);
		border-radius: 2px;
	}
	.wf-bar {
		position: absolute;
		top: 0;
		bottom: 0;
		background: var(--status-healthy);
		border-radius: 2px;
		min-width: 2px;
	}
	.wf-bar.err {
		background: var(--status-critical, #f85149);
	}
</style>
