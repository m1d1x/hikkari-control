<script lang="ts">
	// The current обращение of a support conversation and the four statuses it
	// can be moved to. A change goes through the same PATCH the ticket page
	// uses, which posts the milestone («обращение №N в работе» …) into the
	// user's chat — so what the team clicks is exactly what the user reads.
	import type { SupportCase } from '$lib/api/support';
	import type { TicketStatus } from '$lib/api/tickets';

	interface Props {
		kase: SupportCase;
		busy?: boolean;
		onChange: (status: TicketStatus) => void;
	}

	let { kase, busy = false, onChange }: Props = $props();

	const statuses: { value: TicketStatus; label: string }[] = [
		{ value: 'open', label: 'открыто' },
		{ value: 'analyzed', label: 'в работе' },
		{ value: 'resolved', label: 'решено' },
		{ value: 'dismissed', label: 'закрыто' },
	];

	const typeLabel = $derived(kase.type === 'bug' ? 'баг-репорт' : 'вопрос');
</script>

<section class="bar st-{kase.status}">
	<div class="head">
		<span class="title">обращение №{kase.number}</span>
		<span class="type">{typeLabel}</span>
		{#if kase.type === 'bug'}
			<a class="link" href={`/tickets/${kase.id}`}>тех. данные →</a>
		{/if}
	</div>
	<p class="text">{kase.text}</p>
	<div class="row">
		{#each statuses as s (s.value)}
			<button
				class="btn st-{s.value}"
				class:active={kase.status === s.value}
				disabled={busy || kase.status === s.value}
				onclick={() => onChange(s.value)}
			>
				<span class="dot"></span>
				{s.label}
			</button>
		{/each}
	</div>
	<div class="hint">пользователь увидит смену статуса плашкой в чате</div>
</section>

<style>
	.bar {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 14px 18px;
		margin-bottom: 14px;
		border: 1px solid var(--border);
		border-radius: 12px;
		background: var(--surface);
	}
	.head {
		display: flex;
		align-items: baseline;
		gap: 10px;
	}
	.title {
		font-size: 14px;
		font-weight: 600;
		color: var(--text-primary);
	}
	.type {
		font-size: 11px;
		color: var(--text-muted);
	}
	.link {
		margin-left: auto;
		font-size: 12px;
		color: var(--text-secondary);
		text-decoration: none;
	}
	.link:hover {
		color: var(--text-primary);
	}
	.text {
		margin: 0;
		font-size: 13px;
		color: var(--text-secondary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.row {
		display: flex;
		gap: 8px;
		flex-wrap: wrap;
	}
	.btn {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 7px 14px;
		border-radius: 8px;
		border: 1px solid var(--border);
		background: var(--bg);
		color: var(--text-primary);
		font: inherit;
		font-size: 12px;
		font-weight: 600;
		cursor: pointer;
		transition: all var(--transition-fast);
	}
	.btn:hover:not(:disabled) {
		background: var(--surface-hover);
	}
	.btn:disabled {
		cursor: default;
	}
	.btn:disabled:not(.active) {
		opacity: 0.5;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--text-muted);
	}
	.btn.st-analyzed .dot {
		background: var(--chart-4);
	}
	.btn.st-resolved .dot {
		background: var(--status-healthy);
	}
	.btn.st-dismissed .dot {
		background: transparent;
		border: 1px dashed var(--text-muted);
	}
	.btn.active {
		border-color: var(--text-primary);
		background: var(--surface-hover);
	}
	.hint {
		font-size: 11px;
		color: var(--text-muted);
	}
</style>
