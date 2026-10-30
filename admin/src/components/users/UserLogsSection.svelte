<script lang="ts">
	import { onMount } from 'svelte';
	import LogRow from '$components/logs/LogRow.svelte';
	import {
		fetchAdminLogs,
		fetchUserLogConfig,
		upsertLogConfig,
		deleteLogConfig,
		type AdminLogEntry,
		type LogConfigEntry,
	} from '$lib/api/logs';

	interface Props {
		userId: string;
	}

	let { userId }: Props = $props();

	type Mode = 'all' | 'errors' | 'voice';

	let mode: Mode = $state('all');
	let logs: AdminLogEntry[] = $state([]);
	let loading = $state(false);
	let errorText: string | null = $state(null);
	let expandedKey: string | null = $state(null);

	let traceEntry: LogConfigEntry | null = $state(null);
	let traceBusy = $state(false);
	let traceMsg: string | null = $state(null);

	const VOICE_CATS = ['voice', 'peer', 'signaling', 'sfu'];

	function filterArgs() {
		if (mode === 'errors') return { level: ['error', 'warn'] };
		if (mode === 'voice') return { cat: VOICE_CATS };
		return {};
	}

	async function load() {
		loading = true;
		errorText = null;
		try {
			const res = await fetchAdminLogs({ userId, limit: 50, ...filterArgs() });
			logs = res.entries;
		} catch (e) {
			errorText = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	async function loadTraceStatus() {
		try {
			const res = await fetchUserLogConfig(userId);
			const active = res.entries.find(
				(e) =>
					e.scope === 'user' &&
					e.userId === userId &&
					e.minLevel === 'trace' &&
					e.remoteEnabled &&
					(!e.expiresAt || new Date(e.expiresAt).getTime() > Date.now()),
			);
			traceEntry = active ?? null;
		} catch (e) {
			console.error('[UserLogs] trace status failed:', e);
		}
	}

	function setMode(m: Mode) {
		if (mode === m) return;
		mode = m;
		expandedKey = null;
		load();
	}

	function rowKey(l: AdminLogEntry, i: number): string {
		return `${l.ts}-${i}-${l.msg.slice(0, 16)}`;
	}

	async function setTraceUntil(expiresAt: Date, label: string) {
		traceBusy = true;
		traceMsg = null;
		try {
			await upsertLogConfig({
				scope: 'user',
				userId,
				category: '*',
				minLevel: 'trace',
				remoteEnabled: true,
				expiresAt: expiresAt.toISOString(),
			});
			traceMsg = label;
			await loadTraceStatus();
		} catch (e) {
			traceMsg = 'Ошибка: ' + (e instanceof Error ? e.message : String(e));
		} finally {
			traceBusy = false;
		}
	}

	async function enableTrace() {
		await setTraceUntil(new Date(Date.now() + 60 * 60 * 1000), 'TRACE включён на 1 час');
	}

	async function extendTrace() {
		const base = traceEntry?.expiresAt ? new Date(traceEntry.expiresAt) : new Date();
		const next = new Date(base.getTime() + 60 * 60 * 1000);
		await setTraceUntil(next, 'TRACE продлён на +1 час');
	}

	function fmtRemaining(iso: string | null | undefined): string {
		if (!iso) return '';
		const ms = new Date(iso).getTime() - Date.now();
		if (ms <= 0) return 'истёк';
		const mins = Math.floor(ms / 60000);
		const hrs = Math.floor(mins / 60);
		if (hrs > 0) return `${hrs}ч ${mins % 60}м`;
		return `${mins}м`;
	}

	async function clearTrace() {
		if (!traceEntry) return;
		traceBusy = true;
		traceMsg = null;
		try {
			await deleteLogConfig(traceEntry.id);
			traceMsg = 'TRACE отключён';
			traceEntry = null;
		} catch (e) {
			traceMsg = 'Ошибка: ' + (e instanceof Error ? e.message : String(e));
		} finally {
			traceBusy = false;
		}
	}

	function fmtExpires(iso: string | null | undefined): string {
		if (!iso) return '';
		const d = new Date(iso);
		const hh = String(d.getHours()).padStart(2, '0');
		const mm = String(d.getMinutes()).padStart(2, '0');
		return `${hh}:${mm}`;
	}

	onMount(async () => {
		await Promise.all([load(), loadTraceStatus()]);
	});
</script>

<div class="user-logs">
	<div class="toolbar">
		<div class="tabs">
			<button class="tab" class:active={mode === 'all'} onclick={() => setMode('all')}>
				All
			</button>
			<button class="tab" class:active={mode === 'errors'} onclick={() => setMode('errors')}>
				Errors only
			</button>
			<button class="tab" class:active={mode === 'voice'} onclick={() => setMode('voice')}>
				Voice
			</button>
		</div>
		<div class="right">
			<a class="btn link" href={`/logs?userId=${encodeURIComponent(userId)}`}>
				Open in full log viewer →
			</a>
			{#if traceEntry}
				<span class="trace-badge">
					TRACE active
					{#if traceEntry.expiresAt}
						<span class="mono">
							, осталось {fmtRemaining(traceEntry.expiresAt)}
							(до {fmtExpires(traceEntry.expiresAt)})
						</span>
					{/if}
				</span>
				<button class="btn" onclick={extendTrace} disabled={traceBusy} title="Продлить ещё на 1 час">
					{traceBusy ? '...' : '+1h'}
				</button>
				<button class="btn clear" onclick={clearTrace} disabled={traceBusy} title="Остановить trace немедленно">
					{traceBusy ? '...' : 'Stop now'}
				</button>
			{:else}
				<button class="btn trace" onclick={enableTrace} disabled={traceBusy}>
					{traceBusy ? '...' : 'Set TRACE for this user (1h)'}
				</button>
			{/if}
		</div>
	</div>

	{#if traceMsg}
		<div class="toast">{traceMsg}</div>
	{/if}

	<div class="log-container">
		<div class="log-header">
			<span>Time</span>
			<span>Level</span>
			<span>Source</span>
			<span>Cat</span>
			<span>User</span>
			<span>Message</span>
		</div>
		{#if loading}
			<div class="empty">Загрузка...</div>
		{:else if errorText}
			<div class="empty error-text">Ошибка: {errorText}</div>
		{:else if logs.length === 0}
			<div class="empty">Нет записей</div>
		{:else}
			{#each logs as log, i (rowKey(log, i))}
				{@const k = rowKey(log, i)}
				<LogRow {log} expanded={expandedKey === k} onToggle={() => (expandedKey = expandedKey === k ? null : k)} />
			{/each}
		{/if}
	</div>
</div>

<style>
	.user-logs {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.toolbar {
		display: flex;
		gap: 12px;
		align-items: center;
		flex-wrap: wrap;
	}
	.tabs {
		display: flex;
		gap: 4px;
	}
	.tab {
		padding: 5px 12px;
		border-radius: 5px;
		border: 1px solid var(--border-subtle);
		background: var(--surface, var(--card-bg));
		color: var(--text-secondary);
		font-size: 12px;
		font-family: inherit;
		cursor: pointer;
		transition: background var(--transition-fast);
	}
	.tab:hover {
		background: var(--surface-hover);
	}
	.tab.active {
		background: var(--primary);
		color: var(--bg);
		border-color: var(--primary);
	}
	.right {
		margin-left: auto;
		display: flex;
		gap: 8px;
		align-items: center;
		flex-wrap: wrap;
	}
	.btn {
		padding: 5px 12px;
		border-radius: 5px;
		font-size: 12px;
		font-family: inherit;
		cursor: pointer;
		border: 1px solid var(--border-subtle);
		background: var(--card-bg);
		color: var(--text-primary);
		text-decoration: none;
		transition: background var(--transition-fast);
	}
	.btn:hover:not(:disabled) {
		background: var(--surface-hover);
	}
	.btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.btn.link {
		color: var(--chart-2);
	}
	.btn.trace {
		border-color: var(--status-warning);
		color: var(--status-warning);
	}
	.btn.clear {
		border-color: var(--status-critical);
		color: var(--status-critical);
	}
	.trace-badge {
		font-size: 11px;
		padding: 4px 10px;
		border-radius: 5px;
		background: oklch(0.55 0.15 145 / 0.15);
		color: var(--status-healthy, var(--status-healthy));
		font-weight: 600;
	}
	.mono {
		font-family: var(--font-mono);
		font-weight: 400;
	}
	.toast {
		font-size: 12px;
		color: var(--text-secondary);
		padding: 6px 10px;
		background: var(--card-bg);
		border: 1px solid var(--border-subtle);
		border-radius: 4px;
	}
	.log-container {
		background: oklch(0.1 0 0);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		font-family: var(--font-mono);
		font-size: 12px;
		overflow-y: auto;
		max-height: 480px;
		padding: 4px 0;
	}
	.log-header {
		display: grid;
		grid-template-columns: 92px 52px 72px 90px 80px 1fr 28px;
		gap: 8px;
		padding: 6px 12px;
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		border-bottom: 1px solid var(--border-subtle);
		position: sticky;
		top: 0;
		background: oklch(0.1 0 0);
		z-index: 1;
	}
	.empty {
		padding: 24px 12px;
		text-align: center;
		color: var(--text-muted);
		font-size: 12px;
	}
	.error-text {
		color: var(--status-critical);
	}
</style>
