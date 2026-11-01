<script lang="ts">
	import { onMount } from 'svelte';
	import { ago } from '$lib/product/format';
	import {
		fetchSupportReports,
		replySupportReport,
		updateSupportReportStatus,
		type SupportReport,
		type ProductTicketStatus
	} from '$lib/api/support';

	const STATUS: Record<ProductTicketStatus, string> = {
		open: 'ждёт ответа',
		answered: 'отвечено',
		closed: 'закрыто'
	};

	let list = $state<SupportReport[]>([]);
	let onlyOpen = $state(true);
	let currentId = $state<number | null>(null);
	let selectedMsgIdx = $state<number | null>(null);
	let reply = $state('');
	let loading = $state(false);
	let busy = $state(false);
	let error = $state('');
	let copied = $state(false);
	let copyTimer: ReturnType<typeof setTimeout> | undefined;

	const shown = $derived(list.filter((t) => !onlyOpen || t.status === 'open'));
	const current = $derived(list.find((t) => t.id === currentId) ?? (shown.length > 0 ? shown[0] : null));

	const selectedMsg = $derived(
		current && selectedMsgIdx !== null && current.messages && current.messages[selectedMsgIdx]
			? current.messages[selectedMsgIdx]
			: null
	);

	const activeLogs = $derived(
		selectedMsg?.logs || (selectedMsgIdx === null ? (current?.logs || '') : (selectedMsg ? current?.logs || '' : ''))
	);

	const hasSpecificLogs = $derived(!!selectedMsg?.logs);

	function selectMsg(idx: number) {
		selectedMsgIdx = selectedMsgIdx === idx ? null : idx;
	}

	async function load() {
		loading = true;
		error = '';
		try {
			const res = await fetchSupportReports();
			list = res.reports || [];
			if ((currentId === null || !list.some((t) => t.id === currentId)) && list.length > 0) {
				currentId = list[0].id;
			}
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	onMount(load);

	async function send() {
		if (!current || !reply.trim() || busy) return;
		busy = true;
		try {
			const text = reply.trim();
			const res = await replySupportReport(current.id, text);
			if (res.ok && res.report) {
				const idx = list.findIndex((t) => t.id === current!.id);
				if (idx !== -1) {
					list[idx] = res.report;
				}
			}
			reply = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}

	async function setStatus(st: ProductTicketStatus) {
		if (!current || busy) return;
		busy = true;
		try {
			const res = await updateSupportReportStatus(current.id, st);
			if (res.ok && res.report) {
				const idx = list.findIndex((t) => t.id === current!.id);
				if (idx !== -1) {
					list[idx] = res.report;
				}
			}
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}

	async function copyLogs() {
		if (!activeLogs) return;
		try {
			await navigator.clipboard.writeText(activeLogs);
			copied = true;
			clearTimeout(copyTimer);
			copyTimer = setTimeout(() => {
				copied = false;
			}, 2000);
		} catch (e) {
			console.error('Failed to copy logs', e);
		}
	}
</script>

<div class="head">
	<h1>обращения</h1>
	<div class="chips">
		<button class="chip" class:on={onlyOpen} onclick={() => (onlyOpen = true)}>ждут ответа</button>
		<button class="chip" class:on={!onlyOpen} onclick={() => (onlyOpen = false)}>все</button>
		<button class="btn icon-btn" title="Обновить" onclick={load} disabled={loading}>
			{#if loading}…{:else}⟳{/if}
		</button>
	</div>
</div>

{#if error}
	<div class="error-banner">{error}</div>
{/if}

<div class="support">
	<section class="card list">
		{#each shown as t (t.id)}
			<button class="item" class:on={t.id === current?.id} onclick={() => { currentId = t.id; selectedMsgIdx = null; }}>
				<div class="line">
					<span class="sender">{t.from || t.install_id}</span>
					<span class="dim">{ago(t.messages && t.messages.length > 0 ? t.messages[t.messages.length - 1].ts : new Date(t.created_at).getTime())}</span>
				</div>
				<div class="dim preview">{t.messages && t.messages.length > 0 ? t.messages[t.messages.length - 1].text : t.message}</div>
			</button>
		{:else}
			<p class="empty">{loading ? 'загрузка...' : 'всем ответили'}</p>
		{/each}
	</section>

	<section class="card thread">
		{#if current}
			<div class="meta">
				<b>#{current.id}</b>
				<span class="from">{current.from || current.install_id}</span>
				<span class="dim">{current.platform} · {current.version}</span>
				<span class:warn={current.status === 'open'} class:dim={current.status !== 'open'}>{STATUS[current.status]}</span>
				{#if current.status !== 'closed'}
					<button class="btn" disabled={busy} onclick={() => setStatus('closed')}>закрыть</button>
				{:else}
					<button class="btn" disabled={busy} onclick={() => setStatus('open')}>открыть снова</button>
				{/if}
			</div>

			{#if activeLogs}
				<div class="logs-card">
					<div class="logs-head">
						<span class="logs-title">
							Диагностический лог
							{#if selectedMsgIdx !== null}
								<span class="log-indicator accent">
									· к сообщению #{selectedMsgIdx + 1}
									{#if !hasSpecificLogs}<span class="fallback-note">(снимок из отчёта)</span>{/if}
								</span>
							{:else}
								<span class="log-indicator dim">· самый свежий</span>
							{/if}
						</span>
						<div class="logs-btns">
							{#if selectedMsgIdx !== null}
								<button class="btn reset-btn" onclick={() => (selectedMsgIdx = null)}>
									к последнему
								</button>
							{/if}
							<button class="btn copy-btn" onclick={copyLogs}>
								{copied ? 'Скопировано!' : 'Скопировать логи'}
							</button>
						</div>
					</div>
					<pre class="logs-scroll">{activeLogs}</pre>
				</div>
			{/if}

			<div class="messages">
				{#each current.messages as m, i (i)}
					<div
						class="msg"
						class:staff={m.staff}
						class:interactive={!m.staff}
						class:selected={selectedMsgIdx === i}
						tabindex={!m.staff ? 0 : -1}
						role={!m.staff ? "button" : undefined}
						onclick={() => !m.staff && selectMsg(i)}
						onkeydown={(e) => !m.staff && (e.key === 'Enter' || e.key === ' ') && selectMsg(i)}
					>
						<div class="msg-header">
							<div class="msg-text">{m.text}</div>
							{#if m.logs}
								<span class="log-chip" title="Прикреплён снимок лога">📋 лог</span>
							{/if}
						</div>
						<div class="msg-footer">
							<span class="dim">{ago(m.ts)}</span>
							{#if selectedMsgIdx === i}
								<span class="active-badge">просмотр лога</span>
							{/if}
						</div>
					</div>
				{/each}
			</div>

			{#if current.status !== 'closed'}
				<div class="reply">
					<textarea
						class="field"
						rows="2"
						placeholder="ответ (Ctrl+Enter для отправки)"
						bind:value={reply}
						disabled={busy}
						onkeydown={(e) => e.key === 'Enter' && (e.metaKey || e.ctrlKey) && send()}
					></textarea>
					<button class="btn primary" disabled={busy || !reply.trim()} onclick={send}>отправить</button>
				</div>
			{/if}
		{:else}
			<p class="empty">выбери обращение</p>
		{/if}
	</section>
</div>

<style>
	.head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 16px;
	}

	.chips {
		display: flex;
		gap: 6px;
		align-items: center;
	}

	.icon-btn {
		padding: 4px 8px;
		font-size: 14px;
	}

	.error-banner {
		padding: 8px 12px;
		background: rgba(220, 50, 50, 0.15);
		border: 1px solid var(--bad, #e25555);
		border-radius: 6px;
		color: var(--bad, #e25555);
		font-size: 12px;
		margin-bottom: 16px;
	}

	.support {
		display: grid;
		grid-template-columns: 300px minmax(0, 1fr);
		gap: 16px;
		align-items: start;
	}

	.list {
		padding: 6px;
	}

	.item {
		display: block;
		width: 100%;
		text-align: left;
		padding: 9px 10px;
		border-radius: 6px;
		background: transparent;
		border: none;
		cursor: pointer;
		color: inherit;
	}

	.item:hover,
	.item.on {
		background: var(--surface);
	}

	.line {
		display: flex;
		justify-content: space-between;
		gap: 8px;
	}

	.sender {
		font-weight: 500;
		font-size: 13px;
	}

	.line .dim,
	.preview {
		font-size: 11px;
	}

	.preview {
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		margin-top: 2px;
	}

	.meta {
		display: flex;
		align-items: center;
		gap: 12px;
		padding-bottom: 12px;
		border-bottom: 1px solid var(--border-subtle);
		font-size: 12px;
	}

	.meta .from {
		font-weight: 500;
	}

	.meta .btn {
		margin-left: auto;
	}

	.logs-card {
		margin-top: 12px;
		margin-bottom: 12px;
		padding: 10px 12px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
	}

	.logs-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 8px;
	}

	.logs-title {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--dim, #888);
	}

	.copy-btn {
		font-size: 11px;
		padding: 3px 8px;
	}

	.logs-scroll {
		max-height: 220px;
		overflow-x: auto;
		overflow-y: auto;
		font-family: var(--mono, monospace);
		font-size: 11px;
		line-height: 1.45;
		color: var(--fg, inherit);
		background: rgba(0, 0, 0, 0.25);
		padding: 8px 10px;
		border-radius: 4px;
		margin: 0;
		white-space: pre-wrap;
		word-break: break-all;
	}

	.messages {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 16px 0;
	}

	.msg {
		max-width: 75%;
		padding: 8px 12px;
		background: var(--surface);
		border-radius: 8px;
		border: 1px solid transparent;
	}

	.msg.interactive {
		cursor: pointer;
		transition: background 0.15s, border-color 0.15s, transform 0.1s;
	}

	.msg.interactive:hover {
		background: var(--surface-hover, oklch(0.25 0.02 260));
		border-color: var(--border-subtle);
	}

	.msg.interactive.selected {
		border-color: #38bdf8;
		background: oklch(0.24 0.035 250);
		box-shadow: 0 0 0 1px #38bdf8;
	}

	.msg-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 8px;
	}

	.msg-text {
		word-break: break-word;
	}

	.log-chip {
		flex-shrink: 0;
		font-size: 10px;
		font-weight: 500;
		background: rgba(56, 189, 248, 0.15);
		color: #38bdf8;
		border: 1px solid rgba(56, 189, 248, 0.35);
		border-radius: 4px;
		padding: 1px 5px;
	}

	.msg-footer {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-top: 4px;
	}

	.active-badge {
		font-size: 10px;
		color: #38bdf8;
		font-weight: 500;
	}

	.logs-btns {
		display: flex;
		gap: 6px;
		align-items: center;
	}

	.reset-btn {
		font-size: 11px;
		padding: 3px 8px;
		background: transparent;
		border: 1px dashed var(--border-subtle);
	}

	.log-indicator {
		font-weight: 450;
		text-transform: none;
		letter-spacing: normal;
		margin-left: 4px;
	}

	.log-indicator.accent {
		color: #38bdf8;
	}

	.fallback-note {
		font-size: 10px;
		color: var(--dim, #888);
		margin-left: 4px;
	}

	.msg.staff {
		align-self: flex-end;
		background: oklch(0.29 0.035 350);
	}

	.msg .dim {
		font-size: 10px;
	}

	.reply {
		display: flex;
		gap: 8px;
		align-items: flex-end;
	}

	textarea {
		flex: 1;
		resize: vertical;
		font-family: inherit;
		padding: 8px 10px;
		border-radius: 6px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		color: inherit;
	}

	.empty {
		padding: 16px;
		text-align: center;
		color: var(--dim);
		font-size: 13px;
	}
</style>
