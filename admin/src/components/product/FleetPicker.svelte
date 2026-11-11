<script lang="ts">
	import type { FleetNode } from '$lib/api/billing';

	// A subscription's fleet picked by hand: the logical nodes of the fleet,
	// alive ones first. A node outlives the relay behind it, so a pick stays
	// right when a relay is replaced.
	let { nodes, selected = $bindable([]) }: { nodes: FleetNode[]; selected?: string[] } = $props();

	let filter = $state('');

	const shown = $derived.by(() => {
		const q = filter.trim().toLowerCase();
		if (!q) return nodes;
		return nodes.filter((n) => [n.label, n.slug, n.proto, n.box, n.region].some((v) => v.toLowerCase().includes(q)));
	});

	function toggle(slug: string) {
		selected = selected.includes(slug) ? selected.filter((s) => s !== slug) : [...selected, slug];
	}

	const allShown = () => {
		const add = shown.map((n) => n.slug).filter((s) => !selected.includes(s));
		selected = [...selected, ...add];
	};
</script>

<div class="picker">
	<div class="bar">
		<input class="field" placeholder="фильтр: протокол, страна, машина" bind:value={filter} />
		<button class="chip" type="button" onclick={allShown}>отметить показанные</button>
		<button class="chip" type="button" onclick={() => (selected = [])}>снять все</button>
		<span class="dim">выбрано {selected.length}</span>
	</div>
	{#if nodes.length === 0}
		<p class="dim">в реестре нет узлов</p>
	{:else}
		<div class="list">
			{#each shown as n (n.slug)}
				<label class="node" class:dead={!n.alive}>
					<input type="checkbox" checked={selected.includes(n.slug)} onchange={() => toggle(n.slug)} />
					<span class="label">{n.label || n.slug}</span>
					<span class="mono dim">{n.proto}{n.region ? ` · ${n.region}` : ''}</span>
					<span class="mono dim">{n.box}</span>
					{#if !n.alive}<span class="warn">релей не служит</span>{/if}
				</label>
			{/each}
		</div>
	{/if}
</div>

<style>
	.picker {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.bar {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.bar .field {
		max-width: 280px;
	}

	.list {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
		gap: 2px 16px;
		max-height: 320px;
		overflow: auto;
		padding: 6px;
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
	}

	.node {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 3px 4px;
		font-size: 12px;
		cursor: pointer;
	}

	.node:hover {
		background: oklch(0.21 0 0);
	}

	.node.dead .label {
		color: var(--text-3);
	}

	.label {
		min-width: 120px;
	}

	.warn {
		font-size: 11px;
	}
</style>
