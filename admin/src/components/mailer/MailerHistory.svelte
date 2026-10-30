<script lang="ts">
	import type { MailerLogRow } from '$lib/types/mailer';

	interface Props {
		rows: MailerLogRow[];
		loading: boolean;
		onRefresh: () => void;
	}

	let { rows, loading, onRefresh }: Props = $props();

	function formatTime(iso: string): string {
		const d = new Date(iso);
		const date = d.toLocaleDateString('ru-RU', { day: '2-digit', month: '2-digit' });
		const time = d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
		return `${date} ${time}`;
	}

	// Delivery summary derived from webhook-populated fields. Returns the
	// label, a color tier (ok | warn | bad | neutral), and a tooltip with
	// per-event timestamps so the operator can hover for the full timeline.
	type DeliveryTier = 'ok' | 'warn' | 'bad' | 'neutral';
	interface DeliveryView {
		label: string;
		tier: DeliveryTier;
		tooltip: string;
	}

	function deliveryView(r: MailerLogRow): DeliveryView {
		// Failed-at-send rows never made it to the wire — webhooks don't fire.
		if (r.status === 'failed') {
			return { label: '—', tier: 'neutral', tooltip: 'Не отправлено' };
		}
		// SMTP rows can't get callbacks — show n/a so it's not confused with
		// "delivered but unknown".
		if (r.provider !== 'unisender') {
			return { label: 'n/a', tier: 'neutral', tooltip: 'SMTP не присылает callbacks' };
		}
		// Unisender rows without webhook events yet — pending until the
		// callback lands (usually < 1 min, but bounces can take hours).
		if (!r.last_status) {
			return { label: 'pending', tier: 'neutral', tooltip: 'Ждём webhook от Unisender' };
		}
		const tooltip = buildTimelineTooltip(r);
		switch (r.last_status) {
			case 'delivered':
				return { label: 'delivered', tier: 'ok', tooltip };
			case 'opened':
				return { label: 'opened', tier: 'ok', tooltip };
			case 'clicked':
				return { label: 'clicked', tier: 'ok', tooltip };
			case 'soft_bounced':
				return { label: 'soft bounce', tier: 'warn', tooltip };
			case 'hard_bounced':
				return { label: 'hard bounce', tier: 'bad', tooltip };
			case 'spam':
				return { label: 'spam', tier: 'bad', tooltip };
			case 'unsubscribed':
				return { label: 'unsubscribed', tier: 'warn', tooltip };
			case 'subscribed':
				return { label: 'subscribed', tier: 'ok', tooltip };
			default:
				return { label: r.last_status, tier: 'neutral', tooltip };
		}
	}

	function buildTimelineTooltip(r: MailerLogRow): string {
		const parts: string[] = [];
		if (r.delivered_at) parts.push(`delivered: ${formatTime(r.delivered_at)}`);
		if (r.opened_at) parts.push(`opened: ${formatTime(r.opened_at)}`);
		if (r.clicked_at) parts.push(`clicked: ${formatTime(r.clicked_at)}`);
		if (r.bounced_at) parts.push(`bounced: ${formatTime(r.bounced_at)}`);
		return parts.join(' · ') || `last: ${r.last_status}`;
	}
</script>

<div class="card">
	<div class="hist-header">
		<h3>История отправок</h3>
		<button type="button" class="refresh" disabled={loading} onclick={onRefresh}>
			{loading ? 'Обновление…' : 'Обновить'}
		</button>
	</div>

	{#if rows.length === 0 && !loading}
		<p class="empty">Пока нет отправленных писем.</p>
	{:else}
		<div class="table-wrap">
			<table>
				<thead>
					<tr>
						<th>Время</th>
						<th>Получатель</th>
						<th>Шаблон</th>
						<th>Subject</th>
						<th>Провайдер</th>
						<th>Статус</th>
						<th>Доставка</th>
						<th>Кем</th>
					</tr>
				</thead>
				<tbody>
					{#each rows as r (r.id)}
						{@const dv = deliveryView(r)}
						<tr class:failed={r.status === 'failed'}>
							<td class="time mono">{formatTime(r.sent_at)}</td>
							<td class="recipient">{r.recipient}</td>
							<td>
								{#if r.template_id}
									<span class="tag">{r.template_id}</span>
								{:else}
									<span class="tag raw">raw</span>
								{/if}
							</td>
							<td class="subject" title={r.subject}>{r.subject}</td>
							<td>
								<span class="provider-pill" data-provider={r.provider}>
									{r.provider === 'unisender' ? 'Unisender' : 'SMTP'}
								</span>
							</td>
							<td>
								{#if r.status === 'sent'}
									<span class="status-dot ok"></span> sent
								{:else}
									<span class="status-dot bad"></span>
									<span class="err" title={r.error_msg ?? ''}>failed</span>
								{/if}
							</td>
							<td>
								<span class="delivery-pill" data-tier={dv.tier} title={dv.tooltip}>
									{dv.label}
								</span>
							</td>
							<td class="admin">{r.sent_by_admin_name ?? '—'}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>

<style>
	.card {
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 10px;
		padding: var(--card-padding);
		margin-bottom: var(--card-gap);
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.hist-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
	}

	h3 {
		margin: 0;
		font-size: 14px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.refresh {
		font-size: 12px;
		padding: 6px 12px;
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
		background: transparent;
		color: var(--text-secondary);
		cursor: pointer;
	}

	.refresh:hover:not(:disabled) {
		color: var(--text-primary);
		border-color: var(--primary-muted);
	}

	.refresh:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.empty {
		font-size: 13px;
		color: var(--text-muted);
		text-align: center;
		padding: 24px;
	}

	.table-wrap {
		overflow-x: auto;
	}

	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 13px;
	}

	th {
		text-align: left;
		font-weight: 600;
		color: var(--text-secondary);
		text-transform: uppercase;
		letter-spacing: 0.03em;
		font-size: 11px;
		padding: 6px 12px;
		border-bottom: 1px solid var(--border-subtle);
	}

	td {
		padding: 8px 12px;
		border-bottom: 1px solid var(--border-subtle);
		color: var(--text-primary);
		vertical-align: middle;
	}

	tr.failed td {
		background: rgba(220, 50, 50, 0.04);
	}

	.time {
		color: var(--text-secondary);
		white-space: nowrap;
	}

	.mono {
		font-family: var(--font-mono);
		font-size: 12px;
	}

	.recipient {
		color: var(--text-primary);
	}

	.subject {
		max-width: 320px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.tag {
		display: inline-block;
		padding: 2px 8px;
		font-size: 11px;
		border-radius: 4px;
		background: var(--primary-subtle);
		color: var(--text-primary);
		font-family: var(--font-mono);
	}

	.tag.raw {
		background: rgba(150, 150, 150, 0.15);
		color: var(--text-secondary);
	}

	.provider-pill {
		display: inline-block;
		padding: 2px 8px;
		font-size: 11px;
		border-radius: 4px;
		font-weight: 600;
		letter-spacing: 0.02em;
		font-family: var(--font-mono);
		border: 1px solid var(--border-subtle);
		color: var(--text-secondary);
	}

	.provider-pill[data-provider='unisender'] {
		background: rgba(80, 160, 110, 0.12);
		border-color: rgba(80, 160, 110, 0.4);
		color: var(--text-primary);
	}

	.provider-pill[data-provider='smtp'] {
		background: rgba(120, 130, 200, 0.12);
		border-color: rgba(120, 130, 200, 0.4);
		color: var(--text-primary);
	}

	.delivery-pill {
		display: inline-block;
		padding: 2px 8px;
		font-size: 11px;
		border-radius: 4px;
		font-family: var(--font-mono);
		border: 1px solid var(--border-subtle);
		color: var(--text-secondary);
		cursor: help;
	}

	.delivery-pill[data-tier='ok'] {
		background: rgba(80, 160, 110, 0.12);
		border-color: rgba(80, 160, 110, 0.4);
		color: var(--text-primary);
	}

	.delivery-pill[data-tier='warn'] {
		background: rgba(220, 180, 60, 0.12);
		border-color: rgba(220, 180, 60, 0.4);
		color: var(--text-primary);
	}

	.delivery-pill[data-tier='bad'] {
		background: rgba(220, 70, 70, 0.12);
		border-color: rgba(220, 70, 70, 0.4);
		color: var(--text-primary);
	}

	.delivery-pill[data-tier='neutral'] {
		opacity: 0.7;
	}

	.status-dot {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		margin-right: 6px;
		vertical-align: middle;
	}

	.status-dot.ok {
		background: var(--status-healthy);
	}

	.status-dot.bad {
		background: var(--status-critical);
	}

	.err {
		color: var(--status-critical);
		cursor: help;
		text-decoration: dotted underline;
	}

	.admin {
		color: var(--text-secondary);
		font-size: 12px;
	}
</style>
