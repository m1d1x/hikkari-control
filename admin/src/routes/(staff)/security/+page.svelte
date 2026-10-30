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
		changePassword,
		PASSWORD_MIN_LEN,
		type SecurityState,
	} from '$lib/api/security';

	let sec = $state<SecurityState | null>(null);
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

</script>

<PageShell>
	<PageHeader title="безопасность" description="ваш вход в панель, passkey и коды восстановления" />

	{#if error}<p class="err" role="alert">{error}</p>{/if}

	{#if sec}

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
