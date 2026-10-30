<script lang="ts">
	import {
		BOX_PROTOS,
		createBox,
		describeEntryError,
		fetchHubKey,
		suggestBoxName,
		type BoxProto,
	} from '$lib/api/entry';

	interface Props {
		/** Names already taken, for the suggested name. */
		taken: string[];
		/** A job started: the page follows it. */
		onstarted: (jobId: string) => void;
	}

	let { taken, onstarted }: Props = $props();

	let address = $state('');
	let user = $state('root');
	let password = $state('');
	let provider = $state('');
	let location = $state('');
	let name = $state('');
	let nameTouched = $state(false);
	let rentedUntil = $state('');
	let proto = $state<BoxProto>('vless-reality-vision');
	let region = $state('');
	let domain = $state('');
	let busy = $state(false);
	let error = $state<string | null>(null);

	let hubKey = $state<string | null>(null);
	let copied = $state(false);

	// The name follows the city until the operator types one of their own.
	$effect(() => {
		if (!nameTouched) name = suggestBoxName(location, taken);
	});

	// Each protocol asks for what it needs and nothing else: a voice box its
	// region, an xhttp box the domain whose A record points at the address.
	const protoReady = $derived(
		proto === 'hysteria2' ? region.trim() !== '' : proto === 'vless-xhttp' ? domain.trim() !== '' : true,
	);
	const ready = $derived(
		address.trim() !== '' && provider.trim() !== '' && location.trim() !== '' && name !== '' && protoReady,
	);
	const protoHint = $derived(BOX_PROTOS.find((p) => p.id === proto)?.hint ?? '');

	async function onSubmit(e: SubmitEvent) {
		e.preventDefault();
		if (!ready || busy) return;
		busy = true;
		error = null;
		try {
			const { jobId } = await createBox({
				name,
				address: address.trim(),
				user: user.trim() || 'root',
				password,
				provider: provider.trim(),
				location: location.trim(),
				rentedUntil,
				proto,
				region: proto === 'hysteria2' ? region.trim() : '',
				domain: proto === 'vless-xhttp' ? domain.trim().toLowerCase() : '',
			});
			// The password has done its job on the server; it does not stay here.
			password = '';
			address = '';
			domain = '';
			nameTouched = false;
			onstarted(jobId);
		} catch (err) {
			error = describeEntryError(err);
		} finally {
			busy = false;
		}
	}

	async function showHubKey() {
		try {
			hubKey = (await fetchHubKey()).publicKey;
		} catch (err) {
			error = describeEntryError(err);
		}
	}

	async function copyHubKey() {
		if (!hubKey) return;
		await navigator.clipboard.writeText(hubKey);
		copied = true;
		setTimeout(() => (copied = false), 1500);
	}
</script>

<section class="card">
	<div class="card-head">
		<h2>новая машина</h2>
	</div>
	<form onsubmit={onSubmit} autocomplete="off">
		<div class="grid">
			<label>
				<span>протокол</span>
				<select bind:value={proto} disabled={busy}>
					{#each BOX_PROTOS as p (p.id)}
						<option value={p.id}>{p.label}</option>
					{/each}
				</select>
			</label>
			{#if proto === 'hysteria2'}
				<label>
					<span>регион голоса</span>
					<input bind:value={region} placeholder="ru" class="mono" disabled={busy} />
				</label>
			{:else if proto === 'vless-xhttp'}
				<label>
					<span>домен</span>
					<input bind:value={domain} placeholder="notes.example.xyz" class="mono" disabled={busy} />
				</label>
			{/if}
			<label>
				<span>адрес</span>
				<input bind:value={address} placeholder="203.0.113.7" inputmode="decimal" disabled={busy} />
			</label>
			<label>
				<span>логин</span>
				<input bind:value={user} disabled={busy} />
			</label>
			<label>
				<span>пароль от провайдера</span>
				<input type="password" bind:value={password} autocomplete="new-password" disabled={busy} />
			</label>
			<label>
				<span>провайдер</span>
				<input bind:value={provider} placeholder="vultr" disabled={busy} />
			</label>
			<label>
				<span>город</span>
				<input bind:value={location} placeholder="amsterdam" disabled={busy} />
			</label>
			<label>
				<span>имя</span>
				<input bind:value={name} oninput={() => (nameTouched = true)} placeholder="ams1" class="mono" disabled={busy} />
			</label>
			<label>
				<span>аренда до</span>
				<input type="date" bind:value={rentedUntil} disabled={busy} />
			</label>
		</div>
		<div class="row">
			<button class="btn primary" type="submit" disabled={!ready || busy}>
				{busy ? 'запускаю' : 'развернуть'}
			</button>
			<button class="text" type="button" onclick={showHubKey}>ключ хаба</button>
		</div>
	</form>
	<p class="hint">{protoHint}</p>
	{#if proto === 'vless-xhttp'}
		<p class="hint">До разворота A-запись домена должна указывать на IP машины.</p>
	{/if}
	<p class="hint">
		Пароль используется единоразово для установки SSH-ключа. Либо добавьте ключ хаба в панели хостинга.
	</p>
	{#if hubKey}
		<div class="key">
			<code>{hubKey}</code>
			<button class="text" type="button" onclick={copyHubKey}>{copied ? 'скопировано' : 'скопировать'}</button>
		</div>
	{/if}
	{#if error}<p class="err" role="alert">{error}</p>{/if}
</section>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 12px;
		max-width: 960px;
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

	form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
		gap: 10px 12px;
	}

	label {
		display: flex;
		flex-direction: column;
		gap: 4px;
		font-size: 12px;
		color: var(--text-secondary);
	}

	input,
	select {
		padding: 7px 10px;
		font: inherit;
		font-size: 13px;
		color: var(--text-primary);
		background: var(--surface-hover);
		border: 1px solid var(--border);
		border-radius: 8px;
	}

	input:focus,
	select:focus {
		outline: none;
		border-color: var(--primary);
	}

	.row {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.key {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 10px;
		background: var(--surface-hover);
		border-radius: 8px;
	}

	.key code {
		flex: 1;
		font-family: 'IBM Plex Mono', monospace;
		font-size: 12px;
		overflow-wrap: anywhere;
		color: var(--text-primary);
	}

	.mono {
		font-family: 'IBM Plex Mono', monospace;
	}

	.hint {
		margin: 0;
		font-size: 13px;
		line-height: 1.5;
		color: var(--text-secondary);
	}

	.btn {
		padding: 7px 14px;
		font-size: 13px;
		color: var(--text-primary);
		background: var(--surface-hover);
		border: 1px solid var(--border);
		border-radius: 8px;
		cursor: pointer;
	}

	.btn.primary {
		border-color: var(--primary);
	}

	.btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.text {
		padding: 6px 12px;
		font: inherit;
		font-size: 13px;
		color: var(--text-primary);
		background: none;
		border: none;
		border-radius: 8px;
		cursor: pointer;
	}

	.text:hover {
		background: var(--surface-hover);
	}

	.err {
		margin: 0;
		color: #e25555;
		font-size: 13px;
		overflow-wrap: anywhere;
	}
</style>
