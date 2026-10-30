<script lang="ts">
	import { onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import PageHeader from '$components/layout/PageHeader.svelte';
	import RecoveryCodes from '$components/security/RecoveryCodes.svelte';
	import { formatRelativeTime } from '$lib/utils/format';
	import { describeApiError } from '$lib/api/client';
	import { isCancelled } from '$lib/auth/webauthn';
	import {
		fetchSecurity,
		addPasskey,
		removePasskey,
		regenerateRecoveryCodes,
		fetchScheduledErasures,
		cancelErasure,
		changePassword,
		fetchE2EAlerts,
		ackE2EAlert,
		E2E_REASON_TEXT,
		PASSWORD_MIN_LEN,
		type SecurityState,
		type ScheduledErasure,
		type E2EAlert,
	} from '$lib/api/security';

	let sec = $state<SecurityState | null>(null);
	let erasures = $state<ScheduledErasure[]>([]);
	let e2eAlerts = $state<E2EAlert[]>([]);
	let busy = $state(false);
	let error = $state<string | null>(null);
	let newCodes = $state<string[] | null>(null);
	let confirmRemove = $state<string | null>(null);
	let pwOpen = $state(false);
	let pwNew = $state('');
	let pwRepeat = $state('');
	let pwDone = $state<string | null>(null);

	// Checked before the request so a typo in the repeat never costs a touch.
	const pwMismatch = $derived(pwRepeat !== '' && pwNew !== pwRepeat);
	const pwReady = $derived(pwNew.length >= PASSWORD_MIN_LEN && pwNew === pwRepeat);

	async function load() {
		try {
			sec = await fetchSecurity();
			erasures = (await fetchScheduledErasures()).erasures;
			// Owner-only on the server; support would get a refusal, not a list.
			if (sec.role === 'owner') e2eAlerts = (await fetchE2EAlerts()).alerts;
		} catch (e) {
			error = describeApiError(e);
		}
	}

	onMount(load);

	// Every action below may raise the Touch ID prompt (step-up); a dismissed
	// prompt is not an error worth a red line.
	async function run(action: () => Promise<unknown>) {
		if (busy) return;
		busy = true;
		error = null;
		try {
			await action();
		} catch (e) {
			if (!isCancelled(e)) error = describeApiError(e);
		} finally {
			busy = false;
		}
	}

	const onAdd = () => run(async () => (sec = await addPasskey()));
	const onRemove = (id: string) =>
		run(async () => {
			sec = await removePasskey(id);
			confirmRemove = null;
		});
	const onRegenerate = () => run(async () => (newCodes = (await regenerateRecoveryCodes()).recoveryCodes));
	function closePassword() {
		pwOpen = false;
		pwNew = pwRepeat = '';
	}

	const onChangePassword = () =>
		run(async () => {
			const { sessionsRevoked } = await changePassword(pwNew);
			closePassword();
			pwDone = sessionsRevoked > 0 ? `пароль сменён, другие входы закрыты: ${sessionsRevoked}` : 'пароль сменён';
		});

	const onAckE2E = (id: number) =>
		run(async () => {
			await ackE2EAlert(id);
			e2eAlerts = e2eAlerts.filter((x: E2EAlert) => x.id !== id);
		});

	const onCancelErasure = (id: string) =>
		run(async () => {
			await cancelErasure(id);
			erasures = erasures.filter((x: ScheduledErasure) => x.userId !== id);
		});
</script>

<PageShell>
	<PageHeader title="безопасность" description="ваш вход в панель и то, что ждёт удаления" />

	{#if error}<p class="err" role="alert">{error}</p>{/if}

	{#if sec}
		{#if sec.role === 'owner'}
			<section class="card" class:alarm={e2eAlerts.length > 0}>
				<div class="card-head">
					<h2>подмена ключей</h2>
					<span class="meta">клиент отверг список устройств собеседника, который отдал сервер</span>
				</div>
				{#if e2eAlerts.length === 0}
					<p class="hint">ни одного</p>
				{:else}
					<ul class="list">
						{#each e2eAlerts as a (a.id)}
							<li>
								<div class="e2e">
									<div class="name">
										<span class="sev" class:critical={a.severity === 'critical'}>{a.severity}</span>
										{E2E_REASON_TEXT[a.reason] ?? a.reason}
									</div>
									<div class="meta mono">
										заметил @{a.observerName || a.observerId} · про @{a.peerName || a.peerId}
									</div>
									<div class="meta mono">
										закреплён {a.pinned || '?'} · версия {a.heldVersion} · {a.occurrences} раз · последний {formatRelativeTime(
											a.lastSeen,
										)}
									</div>
									{#if a.detail}<div class="meta mono">{a.detail}</div>{/if}
								</div>
								<button class="btn" disabled={busy} onclick={() => onAckE2E(a.id)}>видел</button>
							</li>
						{/each}
					</ul>
					<p class="hint">
						пользователю ничего не показано. «видел» закрывает алерт, пока то же самое не повторится, и попадает в
						журнал для других владельцев.
					</p>
				{/if}
			</section>
		{/if}

		<section class="card">
			<div class="card-head">
				<h2>passkey</h2>
				<span class="meta">{sec.login} · {sec.role === 'owner' ? 'владелец' : 'поддержка'}</span>
			</div>
			<ul class="list">
				{#each sec.passkeys as k (k.id)}
					<li>
						<div>
							<div class="name">{k.name}</div>
							<div class="meta">
								заведён {formatRelativeTime(k.createdAt)}{k.lastUsedAt
									? ` · последний вход ${formatRelativeTime(k.lastUsedAt)}`
									: ''}
							</div>
						</div>
						{#if sec.passkeys.length > 1}
							{#if confirmRemove === k.id}
								<span class="inline">
									<button class="btn danger" disabled={busy} onclick={() => onRemove(k.id)}>удалить</button>
									<button class="btn" disabled={busy} onclick={() => (confirmRemove = null)}>отмена</button>
								</span>
							{:else}
								<button class="btn" disabled={busy} onclick={() => (confirmRemove = k.id)}>удалить</button>
							{/if}
						{/if}
					</li>
				{/each}
			</ul>
			<p class="hint">
				заведите второй passkey на другом устройстве (айфон, ключ), чтобы потеря одного не закрывала вход. passkey в
				связке icloud уже есть на всех ваших apple-устройствах.
			</p>
			<button class="btn" disabled={busy} onclick={onAdd}>добавить passkey на этом устройстве</button>
		</section>

		<section class="card">
			<div class="card-head">
				<h2>пароль</h2>
			</div>
			{#if pwOpen}
				<form
					class="pw"
					onsubmit={(e) => {
						e.preventDefault();
						if (pwReady) onChangePassword();
					}}
				>
					<input type="text" autocomplete="username" value={sec.login} hidden readonly />
					<label>
						<span>новый пароль, от {PASSWORD_MIN_LEN} символов</span>
						<input type="password" autocomplete="new-password" bind:value={pwNew} disabled={busy} />
					</label>
					<label>
						<span>новый пароль ещё раз</span>
						<input type="password" autocomplete="new-password" bind:value={pwRepeat} disabled={busy} />
					</label>
					{#if pwMismatch}<p class="hint warn">пароли не совпадают</p>{/if}
					<p class="hint">попросит touch id. все другие браузеры, где вы вошли, выйдут.</p>
					<span class="inline">
						<button class="btn" type="submit" disabled={busy || !pwReady}>сменить пароль</button>
						<button class="btn" type="button" disabled={busy} onclick={closePassword}>отмена</button>
					</span>
				</form>
			{:else}
				{#if pwDone}<p class="hint">{pwDone}</p>{/if}
				<button class="btn" disabled={busy} onclick={() => ((pwOpen = true), (pwDone = null))}>сменить пароль</button>
			{/if}
		</section>

		<section class="card">
			<div class="card-head">
				<h2>коды восстановления</h2>
				<span class="meta" class:warn={sec.recoveryCodesLeft <= 3}>осталось {sec.recoveryCodesLeft} из 10</span>
			</div>
			{#if newCodes}
				<p class="hint">старые коды больше не работают. сохраните новые:</p>
				<RecoveryCodes codes={newCodes} ondone={() => ((newCodes = null), load())} />
			{:else}
				<p class="hint">
					вход, когда нет ни одного устройства с passkey. если пропали и они, на сервере: scripts/admin.sh enroll {sec.login}
				</p>
				<button class="btn" disabled={busy} onclick={onRegenerate}>выпустить новые коды</button>
			{/if}
		</section>

		<section class="card">
			<div class="card-head">
				<h2>ждут удаления</h2>
				<span class="meta">аккаунт заблокирован до конца окна, отменить может только панель</span>
			</div>
			{#if erasures.length === 0}
				<p class="hint">никого</p>
			{:else}
				<ul class="list">
					{#each erasures as e (e.userId)}
						<li>
							<div>
								<div class="name">{e.displayName || e.username} <span class="meta">@{e.username}</span></div>
								<div class="meta">удалится {formatRelativeTime(e.eraseAt)} · назначил {e.by || '—'}</div>
							</div>
							<button class="btn" disabled={busy} onclick={() => onCancelErasure(e.userId)}>отменить удаление</button>
						</li>
					{/each}
				</ul>
			{/if}
		</section>
	{/if}
</PageShell>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 12px;
		max-width: 720px;
		margin-bottom: 16px;
		padding: 20px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 12px;
	}

	.card-head {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: 12px;
		flex-wrap: wrap;
	}

	h2 {
		margin: 0;
		font-size: 15px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.list {
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.list li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		padding: 10px 12px;
		background: var(--surface-hover);
		border-radius: 8px;
	}

	.card.alarm {
		border-color: var(--status-warning, #d9a441);
	}

	.e2e {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.mono {
		font-family: 'IBM Plex Mono', monospace;
		overflow-wrap: anywhere;
	}

	.sev {
		margin-right: 6px;
		font-family: 'IBM Plex Mono', monospace;
		font-size: 11px;
		color: var(--status-warning, #d9a441);
	}

	.sev.critical {
		color: #e25555;
	}

	.name {
		font-size: 14px;
		color: var(--text-primary);
	}

	.meta {
		font-size: 12px;
		color: var(--text-secondary);
	}

	.meta.warn {
		color: var(--status-warning, #d9a441);
	}

	.hint {
		margin: 0;
		font-size: 13px;
		line-height: 1.5;
		color: var(--text-secondary);
	}

	.hint.warn {
		color: var(--status-warning, #d9a441);
	}

	.pw {
		display: flex;
		flex-direction: column;
		gap: 10px;
		max-width: 360px;
	}

	.pw label {
		display: flex;
		flex-direction: column;
		gap: 4px;
		font-size: 12px;
		color: var(--text-secondary);
	}

	.pw input {
		padding: 8px 10px;
		font-size: 14px;
		color: var(--text-primary);
		background: var(--surface-hover);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	.inline {
		display: flex;
		gap: 6px;
	}

	.btn {
		align-self: flex-start;
		padding: 8px 12px;
		font-size: 13px;
		color: var(--text-primary);
		background: var(--surface-hover);
		border: 1px solid var(--border);
		border-radius: 8px;
		cursor: pointer;
		white-space: nowrap;
	}

	.btn.danger {
		color: #e25555;
		border-color: #e25555;
	}

	.btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.err {
		color: #e25555;
		font-size: 13px;
	}
</style>
