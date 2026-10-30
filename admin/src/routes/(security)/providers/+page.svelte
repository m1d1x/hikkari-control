<script lang="ts">
	import { onMount } from 'svelte';
	import PageShell from '$components/layout/PageShell.svelte';
	import ProviderCard from '$components/entry/ProviderCard.svelte';
	import {
		fetchProviders,
		fetchHosting,
		fetchHostingInstances,
		fetchProviderCompare,
		formatCents,
		describeProviderError,
		type HostingCatalog,
		type HostingInstance,
		type ProviderAccount,
		type EntryProviderCompare,
		type ProviderStats,
		type DomainSpend,
	} from '$lib/api/entry';

	let providers = $state<ProviderAccount[] | null>(null);
	let domainSpend = $state<DomainSpend | null>(null);
	let catalog = $state<HostingCatalog | null>(null);
	let compare = $state<EntryProviderCompare | null>(null);
	// The hoster's own list next to the hub's records (С1, audit 42 recheck
	// НС2): a machine the hub pays for and no box in service uses is debt the
	// budget may not see — an order whose answer was lost, a retired box the
	// hoster kept. Null until read; a string when the hoster would not answer.
	let instances = $state<HostingInstance[] | null>(null);
	let instancesError = $state<string | null>(null);

	const orphans = $derived((instances ?? []).filter((i) => i.orphan));
	const orphanCents = $derived(orphans.reduce((sum, i) => sum + i.monthlyCents, 0));
	let loadError = $state<string | null>(null);

	// The hosting accounts the owner saved, each with what the fleet measured
	// for it. Only saved ones: a hoster that has machines but no account here
	// (a box rented by hand) has no balance, no limit and no key, and the card
	// used to invent all three for it — "$85.00, limit $100" for the stand's
	// hand-made box (25.10). Its machines still show in the comparison.
	const allHostingProviders = $derived(
		(providers ?? [])
			.filter((p) => p.role === 'hosting' && p.configured)
			.map((p) => ({
				account: p,
				stats: (compare?.hosting ?? []).find((h) => h.provider.toLowerCase() === p.kind.toLowerCase()),
			})),
	);

	const registrarProviders = $derived((providers ?? []).filter((p) => p.role === 'registrar' && p.configured));
	const unconnected = $derived((providers ?? []).filter((p) => !p.configured));

	async function load() {
		try {
			const [provRes, cmp] = await Promise.all([fetchProviders(), fetchProviderCompare()]);
			providers = provRes.providers;
			domainSpend = provRes.domainSpend;
			compare = cmp;
			loadError = null;
		} catch (e) {
			loadError = describeProviderError(e);
		}

		try {
			catalog = (await fetchHosting()).hosting;
		} catch {
			catalog = null;
		}

		if (catalog) {
			try {
				instances = (await fetchHostingInstances()).instances;
				instancesError = null;
			} catch (e) {
				instances = null;
				instancesError = describeProviderError(e);
			}
		}
	}

	function orphanWhy(i: HostingInstance): string {
		if (i.retired) return `машина ${i.box} сдана, хостер её не удалил`;
		if (i.tags?.includes('nekko-entry')) return 'с тегом хаба, но ни одна машина хаба её не называет';
		return 'не машина хаба';
	}

	function replace(a: ProviderAccount) {
		providers = (providers ?? []).map((p) => (p.kind === a.kind ? a : p));
		void load();
	}

	onMount(load);
</script>

<PageShell>
	<div class="entry-scope providers-page">
		<div class="head">
			<h1>провайдеры</h1>
		</div>

		{#if loadError}
			<p class="err-banner" role="alert">{loadError}</p>
		{:else if providers === null}
			<p class="hint">загружаю</p>
		{:else}
			<!-- ======== 1. hosting providers ======== -->
			<div class="sec-head"><h2>хостинг машин</h2></div>

			{#if allHostingProviders.length === 0}
				<p class="hint">ключей хостинга нет: машину заказать неоткуда</p>
			{:else}
				{#each allHostingProviders as item (item.account.kind)}
					<ProviderCard account={item.account} stats={item.stats} onchange={replace} {catalog} />
				{/each}
			{/if}

			{#if catalog}
				<div class="sec-head">
					<h2>машины у хостера</h2>
					{#if orphans.length > 0}
						<span class="debt">долг: {orphans.length} без машины хаба, {formatCents(orphanCents)}/мес</span>
					{/if}
				</div>
				{#if instancesError}
					<p class="err-banner" role="alert">{instancesError}</p>
				{:else if instances === null}
					<p class="hint">загружаю</p>
				{:else if instances.length === 0}
					<p class="hint">у хостера машин нет</p>
				{:else}
					<ul class="instances">
						{#each instances as i (i.id)}
							<li class:orphan={i.orphan}>
								<span class="m name">{i.box || i.label}</span>
								<span class="m">{i.region} · {i.plan}</span>
								<span class="m">{formatCents(i.monthlyCents)}/мес</span>
								<span class="why">{i.orphan ? orphanWhy(i) : i.status}</span>
							</li>
						{/each}
					</ul>
				{/if}
			{/if}

			<!-- ======== 2. registrar providers ======== -->
			<div class="sec-head">
				<h2>регистраторы доменов</h2>
			</div>

			{#if registrarProviders.length === 0}
				<p class="hint">ключей регистратора нет: имена купить неоткуда</p>
			{:else}
				{#each registrarProviders as p (p.kind)}
					<ProviderCard account={p} onchange={replace} {catalog} {domainSpend} />
				{/each}
			{/if}

			<!-- ======== 3. supported, unconnected ======== -->
			{#if unconnected.length > 0}
				<div class="sec-head"><h2>поддерживаются, не подключены</h2></div>
				<div class="unconnected-list">
					{#each unconnected as p (p.kind)}
						<ProviderCard account={p} onchange={replace} {catalog} />
					{/each}
				</div>
			{/if}
		{/if}
	</div>
</PageShell>

<style>
	.sec-head {
		display: flex;
		align-items: baseline;
		gap: 12px;
		margin: 20px 2px 10px;
	}

	.sec-head h2 {
		margin: 0;
		font-size: 14px;
		font-weight: 600;
	}

	.debt {
		font-size: 13px;
		color: var(--bad);
	}

	.instances {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.instances li {
		display: grid;
		grid-template-columns: minmax(80px, 1fr) minmax(120px, 1fr) 90px 2fr;
		gap: 12px;
		padding: 8px 12px;
		font-size: 13px;
		color: var(--text-2);
	}

	.instances li.orphan .why {
		color: var(--bad);
	}

	.instances .name {
		color: var(--text);
	}

	.unconnected-list {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
</style>
