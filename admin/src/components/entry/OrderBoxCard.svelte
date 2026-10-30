<script lang="ts">
	import { onMount } from 'svelte';
	import {
		BOX_PROTOS,
		fetchHosting,
		formatCents,
		orderBox,
		describeProviderError,
		type BoxProto,
		type HostingCatalog,
	} from '$lib/api/entry';

	// Renting a machine (roadmap 42 П9.2). The difference from «новая машина»
	// next to it is who rents: there the owner did it in a panel and typed the
	// address in, here the hub orders it, waits for the address and runs the
	// same provisioning job. Everything after the order is identical, the
	// admission probe from inside Russia included.

	interface Props {
		/** A job started: the page follows it. */
		onstarted: (jobId: string) => void;
	}

	let { onstarted }: Props = $props();

	let catalog = $state<HostingCatalog | null>(null);
	let unavailable = $state<string | null>(null);
	let region = $state('');
	let plan = $state('');
	let name = $state('');
	let proto = $state<BoxProto>('vless-reality-vision');
	let voiceRegion = $state('');
	let busy = $state(false);
	let error = $state<string | null>(null);
	let note = $state<string | null>(null);

	onMount(async () => {
		try {
			catalog = (await fetchHosting()).hosting;
			region = catalog.defaultRegion ?? '';
			plan = catalog.defaultPlan ?? '';
		} catch (e) {
			unavailable = describeProviderError(e);
		}
	});

	// Only the plans sold where the machine is being ordered: a plan out of
	// its region fails at the hoster, and the form should not offer it.
	const plansHere = $derived((catalog?.plans ?? []).filter((p) => region === '' || p.regions.includes(region)));
	const chosen = $derived(plansHere.find((p) => p.id === plan));
	const spend = $derived(catalog?.spend);
	// The money gate, said before the button is pressed rather than after the
	// server refuses: the hosting group's day and month with this machine
	// running to their ends, as the hub reads them (spend.go). The hub's own
	// check is the one that counts; this one only spares a refused press.
	const added = $derived.by(() => {
		if (!chosen) return null;
		const now = new Date();
		const hourly = chosen.monthlyCents / 672;
		const dayEnd = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + 1);
		const monthEnd = Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + 1, 1);
		const hoursTo = (end: number) => Math.max(1, Math.ceil((end - now.getTime()) / 3_600_000));
		return { day: hoursTo(dayEnd) * hourly, month: Math.min(672, hoursTo(monthEnd)) * hourly };
	});
	const overDay = $derived(!!added && !!spend && spend.dayProjectedCents + added.day > spend.dailyCapCents);
	const overMonth = $derived(
		!!added && !!spend && spend.monthProjectedCents + added.month > spend.monthlyCapCents,
	);
	const protoReady = $derived(proto === 'hysteria2' ? voiceRegion.trim() !== '' : true);
	const ready = $derived(region !== '' && plan !== '' && protoReady && !overDay && !overMonth && !busy);

	async function onSubmit(e: SubmitEvent) {
		e.preventDefault();
		if (!ready) return;
		busy = true;
		error = note = null;
		try {
			const res = await orderBox({
				region,
				plan,
				name: name.trim(),
				proto,
				voiceRegion: proto === 'hysteria2' ? voiceRegion.trim() : '',
			});
			note = `машина ${res.box} заказана, разворот пошёл`;
			name = '';
			onstarted(res.jobId);
			catalog = (await fetchHosting()).hosting;
		} catch (err) {
			error = describeProviderError(err);
		} finally {
			busy = false;
		}
	}
</script>

<section class="card">
	<div class="card-head">
		<h2>заказать машину</h2>
		{#if catalog}
			<span class="meta">{catalog.kind}</span>
		{/if}
	</div>

	{#if unavailable}
		<p class="hint">
			хостер не подключён: {unavailable}
		</p>
	{:else if catalog === null}
		<p class="hint">загружаю предложение хостера</p>
	{:else}
		<form onsubmit={onSubmit} autocomplete="off">
			<div class="grid">
				<label>
					<span>регион</span>
					<select bind:value={region} disabled={busy}>
						<option value="">выбрать</option>
						{#each catalog.regions as r (r.id)}
							<option value={r.id}>{r.city}, {r.country}</option>
						{/each}
					</select>
				</label>
				<label>
					<span>план</span>
					<select bind:value={plan} disabled={busy}>
						<option value="">выбрать</option>
						{#each plansHere as p (p.id)}
							<option value={p.id}>
								{p.vcpus} cpu · {p.ramMb} мб · {formatCents(p.monthlyCents)}/мес
							</option>
						{/each}
					</select>
				</label>
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
						<input bind:value={voiceRegion} placeholder="ru" class="mono" disabled={busy} />
					</label>
				{/if}
				<label>
					<span>имя</span>
					<input bind:value={name} placeholder="из города региона" class="mono" disabled={busy} />
				</label>
			</div>

			{#if spend}
				<p class="hint">
					хостинг за день выйдет в {formatCents(spend.dayProjectedCents)} из {formatCents(spend.dailyCapCents)},
					за месяц в {formatCents(spend.monthProjectedCents)} из {formatCents(spend.monthlyCapCents)}
					{#if overDay}
						· эта машина не влезает в потолок дня
					{:else if overMonth}
						· эта машина не влезает в потолок месяца
					{/if}
				</p>
			{/if}

			<div class="row">
				<button class="btn primary" type="submit" disabled={!ready}>
					{busy ? 'заказываю' : chosen ? `заказать за ${formatCents(chosen.monthlyCents)}/мес` : 'заказать'}
				</button>
			</div>
		</form>
		<p class="hint">
			ключ хаба уедет хостеру до заказа, машина поднимется уже с ним — пароль не нужен. в пул релей войдёт только с
			чистым вердиктом пробника из рф.
		</p>
	{/if}

	{#if error}<p class="err" role="alert">{error}</p>{/if}
	{#if note}<p class="note">{note}</p>{/if}
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

	.meta {
		font-size: 12px;
		color: var(--text-secondary);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
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

	.mono {
		font-family: 'IBM Plex Mono', monospace;
	}

	.row {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-top: 12px;
	}

	.btn {
		padding: 6px 12px;
		font-size: 13px;
		color: var(--text-primary);
		background: var(--surface-hover);
		border: 1px solid var(--border);
		border-radius: 8px;
		cursor: pointer;
		white-space: nowrap;
	}

	.btn.primary {
		border-color: var(--primary);
	}

	.btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.hint {
		margin: 0;
		font-size: 12px;
		line-height: 1.5;
		color: var(--text-secondary);
	}

	.err {
		margin: 0;
		font-size: 13px;
		color: #e25555;
		overflow-wrap: anywhere;
	}

	.note {
		margin: 0;
		font-size: 13px;
		color: var(--status-success, #3fb950);
	}
</style>
