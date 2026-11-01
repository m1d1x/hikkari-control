<script lang="ts">
	import { goto } from '$app/navigation';
	import {
		loginAdmin,
		loginWithPasskey,
		loginWithRecoveryCode,
		enrollFirstPasskey,
		type StepResult,
	} from '$lib/auth/login';
	import { isCancelled } from '$lib/auth/webauthn';
	import { accessDenied } from '$lib/stores/access';
	import RecoveryCodes from '$components/security/RecoveryCodes.svelte';

	// password → passkey (Touch ID fires at once) → in.
	// First time: password → console code → Touch ID registers → save codes → in.
	// No device at hand: password → recovery code → in.
	type Step = 'password' | 'passkey' | 'recovery' | 'enroll' | 'codes' | 'not-enrolled';

	let step = $state<Step>('password');
	let username = $state('');
	let password = $state('');
	let code = $state('');
	let busy = $state(false);
	let error = $state<string | null>(null);
	let ceremonyId = '';
	let passkeyOptions: unknown = null;
	let shownCodes = $state<string[]>([]);
	let lowCodesLeft = $state<number | null>(null);

	function lockedText(seconds: number): string {
		return `слишком много попыток. повторите через ${Math.max(1, Math.ceil(seconds / 60))} мин.`;
	}

	function backToPassword(message: string) {
		step = 'password';
		code = '';
		error = message;
	}

	async function signedIn() {
		accessDenied.set(false);
		await goto('/', { replaceState: true });
	}

	// Shared outcome handling for every second step.
	async function handleStep(r: StepResult, wrongText: string) {
		switch (r.kind) {
			case 'ok':
				if (r.signedIn.recoveryCodes?.length) {
					shownCodes = r.signedIn.recoveryCodes;
					step = 'codes';
					return;
				}
				if (r.signedIn.recoveryCodesLeft !== undefined && r.signedIn.recoveryCodesLeft <= 3) {
					lowCodesLeft = r.signedIn.recoveryCodesLeft;
					return;
				}
				await signedIn();
				return;
			case 'invalid':
				error = wrongText;
				return;
			case 'locked':
				backToPassword(lockedText(r.retryAfterSeconds));
				return;
			case 'expired':
				backToPassword('время на подтверждение вышло, войдите ещё раз');
				return;
			case 'error':
				error = `ошибка: ${r.message}`;
				return;
		}
	}

	async function submitPassword(e: Event) {
		e.preventDefault();
		if (busy) return;
		error = null;
		busy = true;
		try {
			const r = await loginAdmin(username, password);
			switch (r.kind) {
				case 'passkey':
					ceremonyId = r.ceremonyId;
					passkeyOptions = r.options;
					step = 'passkey';
					await touchPasskey();
					return;
				case 'enroll':
					ceremonyId = r.ceremonyId;
					step = 'enroll';
					return;
				case 'not-enrolled':
					step = 'not-enrolled';
					return;
				case 'invalid':
					error = 'неверный логин или пароль';
					return;
				case 'forbidden':
					error = 'нет прав администратора';
					return;
				case 'locked':
					error = lockedText(r.retryAfterSeconds);
					return;
				case 'expired':
				case 'error':
					error = r.kind === 'error' ? `ошибка: ${r.message}` : 'попробуйте ещё раз';
					return;
			}
		} finally {
			busy = false;
		}
	}

	async function touchPasskey() {
		error = null;
		busy = true;
		try {
			await handleStep(await loginWithPasskey(ceremonyId, passkeyOptions), 'passkey не подошёл');
		} catch (e) {
			// Dismissing the system prompt is a choice, not an error: leave the
			// buttons to retry or switch to a recovery code.
			if (!isCancelled(e)) error = `ошибка: ${e instanceof Error ? e.message : String(e)}`;
		} finally {
			busy = false;
		}
	}

	async function submitRecovery(e: Event) {
		e.preventDefault();
		if (busy || !code) return;
		error = null;
		busy = true;
		try {
			await handleStep(await loginWithRecoveryCode(ceremonyId, code), 'код не подошёл или уже использован');
		} finally {
			busy = false;
		}
	}

	async function submitEnroll(e: Event) {
		e.preventDefault();
		if (busy || !code) return;
		error = null;
		busy = true;
		try {
			await handleStep(await enrollFirstPasskey(ceremonyId, code), 'код не подошёл, проверьте вывод консоли');
		} catch (e) {
			if (!isCancelled(e)) error = `ошибка: ${e instanceof Error ? e.message : String(e)}`;
		} finally {
			busy = false;
		}
	}
</script>

<div class="login-screen">
	<div class="login-container">
		<div class="login-brand">
			<svg class="login-brand-icon" viewBox="0 0 1101.2 947.8" fill="none" aria-hidden="true">
				<path d="M720.6 62.6C787.5 101.2 841.4 155.1 880 222C918.6 288.9 937.9 364.7 937.9 440.6C937.9 516.4 918.6 592.3 880 659.2C841.4 726.1 787.5 780 720.6 818.6L678.9 746.4C733.9 714.6 778.2 670.3 810 615.3C841.8 560.3 857.7 497.9 857.7 440.6C857.7 383.2 841.8 320.8 810 265.8C778.2 210.8 733.9 166.5 678.9 134.7L720.6 62.6Z" fill="currentColor"/>
				<path d="M574.6 146.9C629.6 178.7 673.9 223 705.7 278C737.5 333 753.4 395.4 753.4 452.8C753.4 510.1 737.5 572.5 705.7 627.5C673.9 682.5 629.6 726.8 574.6 758.6L532.9 686.4C576 661.5 610.7 626.8 635.6 583.7C660.5 540.6 673 491.7 673 452.8C673 413.8 660.5 364.9 635.6 321.8C610.7 278.7 576 244 532.9 219.1L574.6 146.9Z" fill="currentColor"/>
				<path d="M428.6 231.2C471.7 256.1 506.4 290.8 531.3 333.9C556.2 377 568.7 425.9 568.7 464.9C568.7 503.9 556.2 552.8 531.3 595.9C506.4 639 471.7 673.7 428.6 698.6L386.9 626.4C418.1 608.4 443.2 583.3 461.2 552.1C479.2 520.9 488.3 485.5 488.3 464.9C488.3 444.3 479.2 408.8 461.2 377.7C443.2 346.5 418.1 321.4 386.9 303.4L428.6 231.2Z" fill="currentColor"/>
				<path d="M282.6 315.6C313.8 333.6 338.9 358.7 356.9 389.9C374.9 421.1 384 456.5 384 477.1C384 497.7 374.9 533.1 356.9 564.3C338.9 595.5 313.8 620.6 282.6 638.6L240.9 566.4C260.2 555.2 275.8 539.6 287 520.3C298.2 501 303.8 479 303.8 477.1C303.8 475.2 298.2 453.2 287 433.9C275.8 414.6 260.2 399 240.9 387.8L282.6 315.6Z" fill="currentColor"/>
			</svg>
			<span class="login-brand-name">hikkari</span>
			<span class="login-brand-tag">admin</span>
		</div>

		{#if step === 'password'}
		<form class="login-card" onsubmit={submitPassword}>
			<h1 class="login-title">вход в админку</h1>
			<p class="login-sub">пароль, затем passkey</p>

			<label class="login-field">
				<span>логин</span>
				<input type="text" autocomplete="username webauthn" bind:value={username} disabled={busy} required />
			</label>

			<label class="login-field">
				<span>пароль</span>
				<input type="password" autocomplete="current-password" bind:value={password} disabled={busy} required />
			</label>

			{#if error}<p class="login-error" role="alert">{error}</p>{/if}

			<button class="login-btn" type="submit" disabled={busy || !username || !password}>
				{busy ? 'входим…' : 'войти'}
			</button>
		</form>
	{:else if step === 'passkey'}
		<div class="login-card">
			<h1 class="login-title">подтвердите passkey</h1>
			<p class="login-sub">touch id или ключ, которым вы заводили вход</p>

			{#if lowCodesLeft !== null}
				<p class="login-warn">
					осталось кодов восстановления: {lowCodesLeft}. выпустите новые в разделе «безопасность».
				</p>
				<button class="login-btn" type="button" onclick={signedIn}>понятно</button>
			{:else}
				{#if error}<p class="login-error" role="alert">{error}</p>{/if}
				<button class="login-btn primary" type="button" onclick={touchPasskey} disabled={busy}>
					{busy ? 'ждём подтверждения…' : 'подтвердить'}
				</button>
				<button class="login-link" type="button" onclick={() => ((step = 'recovery'), (error = null))} disabled={busy}>
					нет устройства под рукой, ввести код восстановления
				</button>
			{/if}
		</div>
	{:else if step === 'recovery'}
		<form class="login-card" onsubmit={submitRecovery}>
			<h1 class="login-title">код восстановления</h1>
			<p class="login-sub">один из десяти кодов, сохранённых при заведении. каждый работает один раз.</p>

			{#if lowCodesLeft !== null}
				<p class="login-warn">
					осталось кодов восстановления: {lowCodesLeft}. выпустите новые в разделе «безопасность».
				</p>
				<button class="login-btn" type="button" onclick={signedIn}>понятно</button>
			{:else}
				<label class="login-field">
					<span>код</span>
					<input
						class="mono"
						type="text"
						autocomplete="one-time-code"
						placeholder="XXXX-XXXX-XXXX"
						bind:value={code}
						disabled={busy}
						required
					/>
				</label>
				{#if error}<p class="login-error" role="alert">{error}</p>{/if}
				<button class="login-btn" type="submit" disabled={busy || !code}>{busy ? 'проверяем…' : 'войти'}</button>
				<button class="login-link" type="button" onclick={() => ((step = 'passkey'), (error = null))} disabled={busy}>
					назад к passkey
				</button>
			{/if}
		</form>
	{:else if step === 'enroll'}
		<form class="login-card" onsubmit={submitEnroll}>
			<h1 class="login-title">заведите passkey</h1>
			<p class="login-sub">
				введите код, который напечатала консоль на сервере (admin-create или admin-2fa-reset), затем подтвердите touch
				id.
			</p>
			<label class="login-field">
				<span>код из консоли</span>
				<input
					class="mono"
					type="text"
					autocomplete="one-time-code"
					placeholder="XXXX-XXXX-XXXX"
					bind:value={code}
					disabled={busy}
					required
				/>
			</label>
			{#if error}<p class="login-error" role="alert">{error}</p>{/if}
			<button class="login-btn primary" type="submit" disabled={busy || !code}>
				{busy ? 'ждём подтверждения…' : 'завести passkey'}
			</button>
		</form>
	{:else if step === 'codes'}
		<div class="login-card wide">
			<h1 class="login-title">сохраните коды восстановления</h1>
			<p class="login-sub">
				это ваш запасной вход, если пропадут все устройства с passkey. каждый код работает один раз. больше их никто не
				покажет.
			</p>
			<RecoveryCodes codes={shownCodes} ondone={signedIn} doneLabel="войти" />
		</div>
	{:else if step === 'not-enrolled'}
		<div class="login-card">
			<h1 class="login-title">нет второго фактора</h1>
			<p class="login-sub">
				у этого входа нет passkey и нет кода для заведения. выпустите код на сервере и войдите снова:
			</p>
			<pre class="login-cmd">scripts/admin.sh enroll {username || '<логин>'}</pre>
			<button class="login-btn" type="button" onclick={() => backToPassword('')}>назад</button>
		</div>
	{/if}
	</div>
</div>

<style>
	.login-screen {
		display: flex;
		align-items: center;
		justify-content: center;
		min-height: 100vh;
		padding: 16px;
		box-sizing: border-box;
		background: var(--bg);
	}

	.login-container {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 20px;
		width: 100%;
		max-width: 380px;
	}

	.login-container:has(.wide) {
		max-width: 460px;
	}

	.login-brand {
		display: flex;
		align-items: center;
		gap: 10px;
	}

	.login-brand-icon {
		width: 22px;
		height: 22px;
		color: var(--primary);
		flex-shrink: 0;
	}

	.login-brand-name {
		font-family: 'IBM Plex Mono', monospace;
		font-size: 16px;
		font-weight: 700;
		letter-spacing: 0.1em;
		color: var(--text-primary);
		text-transform: lowercase;
	}

	.login-brand-tag {
		font-family: 'IBM Plex Mono', monospace;
		font-size: 10px;
		font-weight: 600;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--primary);
		background: var(--primary-glow);
		padding: 2px 7px;
		border-radius: 4px;
	}

	.login-card {
		display: flex;
		flex-direction: column;
		gap: 16px;
		width: 100%;
		padding: 36px 32px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 12px;
		box-sizing: border-box;
	}

	.login-card.wide {
		width: 100%;
	}

	.login-title {
		font-size: 20px;
		font-weight: 600;
		color: var(--text-primary);
		margin: 0;
	}

	.login-sub {
		font-size: 13px;
		line-height: 1.5;
		color: var(--text-secondary);
		margin: 0 0 8px;
	}

	.login-field {
		display: flex;
		flex-direction: column;
		gap: 6px;
		font-size: 13px;
		color: var(--text-secondary);
	}

	.login-field input {
		padding: 10px 12px;
		font-size: 14px;
		color: var(--text-primary);
		background: var(--surface-hover);
		border: 1px solid var(--border);
		border-radius: 8px;
		outline: none;
	}

	.login-field input.mono {
		font-family: 'IBM Plex Mono', monospace;
		letter-spacing: 0.04em;
		text-transform: uppercase;
	}

	.login-field input:focus {
		border-color: var(--text-primary);
	}

	.login-error {
		font-size: 13px;
		color: #e25555;
		margin: 0;
	}

	.login-warn {
		font-size: 13px;
		line-height: 1.5;
		color: var(--status-warning, #d9a441);
		margin: 0;
	}

	.login-cmd {
		margin: 0;
		padding: 10px 12px;
		font-family: 'IBM Plex Mono', monospace;
		font-size: 13px;
		color: var(--text-primary);
		background: var(--surface-hover);
		border-radius: 8px;
		overflow-x: auto;
	}

	.login-btn {
		padding: 10px 16px;
		font-size: 14px;
		font-weight: 500;
		color: var(--text-primary);
		background: var(--surface-hover);
		border: 1px solid var(--border);
		border-radius: 8px;
		cursor: pointer;
		transition: background 0.15s;
	}

	.login-btn.primary {
		border-color: var(--text-primary);
	}

	.login-btn:hover:not(:disabled) {
		background: var(--surface-active);
	}

	.login-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.login-link {
		padding: 0;
		font-size: 13px;
		color: var(--text-secondary);
		background: none;
		border: none;
		text-align: left;
		cursor: pointer;
		text-decoration: underline;
		text-underline-offset: 3px;
	}

	.login-link:hover:not(:disabled) {
		color: var(--text-primary);
	}
</style>
