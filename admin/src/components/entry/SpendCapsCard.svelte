<script lang="ts">
	import { untrack } from 'svelte';
	import type { SpendReading } from '$lib/api/entry';
	interface Props {
		reading: SpendReading;
		busy: boolean;
		saved: boolean;
		onsave: (daily: number, monthly: number) => Promise<void>;
	}
	let { reading, busy, saved, onsave }: Props = $props();
	let daily = $state<number | undefined>(0);
	let monthly = $state<number | undefined>(0);
	let baseline: { role: SpendReading['role']; daily: number; monthly: number } | undefined;
	$effect(() => {
		const next = { role: reading.role, daily: reading.dailyCapCents / 100, monthly: reading.monthlyCapCents / 100 };
		untrack(() => {
			// A response refreshes both groups. Keep an edited draft until its own save.
			if (!baseline || baseline.role !== next.role || (daily === baseline.daily && monthly === baseline.monthly)) {
				daily = next.daily;
				monthly = next.monthly;
			}
			baseline = next;
		});
	});
	const dirty = $derived(daily !== reading.dailyCapCents / 100 || monthly !== reading.monthlyCapCents / 100);
	const valid = $derived(
		daily !== undefined &&
			monthly !== undefined &&
			Number.isFinite(daily) &&
			Number.isFinite(monthly) &&
			daily >= 0 &&
			monthly >= daily,
	);
	const money = (v: number) => (v / 100).toLocaleString('ru-RU', { style: 'currency', currency: 'USD' });
</script>

<form
	onsubmit={(event) => {
		event.preventDefault();
		if (valid && !busy) void onsave(Math.round(daily! * 100), Math.round(monthly! * 100));
	}}
>
	<h3>{reading.role === 'hosting' ? 'Все хостеры' : 'Все регистраторы доменов'}</h3>
	<p>
		День: <b>{money(reading.dayCents)}</b> из {money(reading.dailyCapCents)} · прогноз {money(
			reading.dayProjectedCents,
		)}
	</p>
	<p>
		Месяц: <b>{money(reading.monthCents)}</b> из {money(reading.monthlyCapCents)} · прогноз {money(
			reading.monthProjectedCents,
		)}
	</p>
	{#if reading.dayProjectedCents > reading.dailyCapCents || reading.monthProjectedCents > reading.monthlyCapCents}<p
			class="warning"
		>
			Прогноз выше бюджета. Новые покупки приостановлены; работающие машины продолжают оплачиваться.
		</p>{/if}
	<div class="fields">
		<label>Лимит $ / день<input type="number" min="0" step="0.01" required bind:value={daily} disabled={busy} /></label>
		<label
			>Лимит $ / месяц<input type="number" min="0" step="0.01" required bind:value={monthly} disabled={busy} /></label
		>
	</div>
	<button type="submit" disabled={busy || !valid || !dirty}>{busy ? 'Сохраняю…' : 'Сохранить лимиты'}</button>
	{#if dirty}<span class="pending" role="status">Есть несохранённые изменения</span>
	{:else if saved}<span role="status">Сохранено</span>{/if}
	<p class="note">0 запрещает новые покупки. Прогноз хостинга учитывает аренду работающих машин до конца периода.</p>
</form>

<style>
	h3 {
		margin: 0 0 12px;
		font-size: 14px;
	}
	p {
		font-size: 12px;
		color: var(--text-2);
		line-height: 1.6;
	}
	.fields {
		display: flex;
		gap: 12px;
		margin: 14px 0;
	}
	label {
		display: grid;
		gap: 6px;
		flex: 1;
		min-width: 0;
		font-size: 12px;
	}
	input {
		width: 100%;
		box-sizing: border-box;
		background: var(--inset);
		color: var(--text);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 8px;
	}
	button {
		background: var(--primary);
		color: white;
		border: 0;
		border-radius: 6px;
		padding: 8px 12px;
		cursor: pointer;
	}
	button:disabled {
		opacity: 0.5;
		cursor: default;
	}
	span {
		margin-left: 10px;
		font-size: 12px;
		color: var(--ok);
	}
	.pending {
		color: var(--warn);
	}
	.note {
		color: var(--text-3);
	}
	.warning {
		color: var(--warn);
	}
</style>
