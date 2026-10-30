<script lang="ts">
	import { buildState, type EdgeBuild, type PoolBox, type PoolRelay } from '$lib/api/entry';

	interface Props {
		/** null: the relays no imported box claims. */
		box: PoolBox | null;
		relays: PoolRelay[];
		/** Extra line under the head (e.g. the Censor verdict summary). */
		note?: string;
		noteTone?: 'ok' | 'warn' | 'bad' | 'mute';
		/** The hub's relay builds; given, the head says which version the box runs. */
		builds?: EdgeBuild[];
	}

	let { box, relays, note, noteTone = 'mute', builds }: Props = $props();

	const build = $derived(box && builds ? buildState(box, builds) : null);

	const alive = $derived(relays.filter((r) => r.status === 'alive').length);

	// Days of rental left — the number the owner acts on (renew or let go).
	function rentText(iso?: string): string | null {
		if (!iso) return null;
		const days = Math.ceil((new Date(iso).getTime() - Date.now()) / 86_400_000);
		if (days < 0) return 'аренда истекла';
		return `аренда ещё ${days} дн`;
	}

	const rent = $derived(rentText(box?.rentedUntil));
	const rentWarn = $derived(
		box?.rentedUntil ? new Date(box.rentedUntil).getTime() - Date.now() < 7 * 86_400_000 : false,
	);
</script>

<div class="head">
	{#if box}
		<span class="name mono">{box.name}</span>
		<span class="provider">{box.provider}</span>
		<span class="meta">{box.location}</span>
		<span class="meta mono">{box.unreadable ? 'адрес не открылся' : box.address}</span>
		{#if rent}<span class="meta" class:warn={rentWarn}>{rent}</span>{/if}
		{#if build === 'unknown'}
			<span class="meta">версия релея неизвестна</span>
		{:else if build}
			<span class="meta mono" class:warn={build === 'behind'} title={box.edgeSha}
				>{box.arch} {box.edgeSha?.slice(0, 12)}{build === 'behind' ? ' · старая версия' : ''}</span
			>
		{/if}
	{:else}
		<span class="name">без машины</span>
		<span class="meta">адрес не совпал ни с одной машиной из inventory.json</span>
	{/if}
	<span class="count meta">живых {alive} из {relays.length}</span>
</div>
{#if note}<p class="note {noteTone}">{note}</p>{/if}

<style>
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 4px 12px;
	}

	.name {
		font-size: 14px;
		font-weight: 600;
	}

	.provider {
		font-size: 13px;
		text-transform: lowercase;
	}

	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}

	.meta {
		font-size: 12px;
		color: var(--text-muted);
	}

	.warn {
		color: var(--warning, #f5a623);
	}

	.count {
		margin-left: auto;
	}

	.note {
		margin: 4px 0 0;
		font-size: 12px;
	}

	.note.ok {
		color: var(--success, #30a46c);
	}

	.note.warn {
		color: var(--warning, #f5a623);
	}

	.note.bad {
		color: var(--danger, #e5484d);
	}

	.note.mute {
		color: var(--text-muted);
	}
</style>
