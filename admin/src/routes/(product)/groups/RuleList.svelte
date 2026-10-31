<script lang="ts">
	interface Props {
		label: string;
		hint: string;
		items: string[];
		/** the canonical form the client stores, or a refusal it would give */
		normalize: (value: string) => Promise<string>;
	}

	let { label, hint, items = $bindable(), normalize }: Props = $props();
	let input = $state('');
	let refused = $state<string[]>([]);

	/**
	 * Adds every value in text, split on newlines and commas (paths and app
	 * names hold spaces), each in the form the client stores, skipping ones
	 * already there. What the client would refuse stays in the field, named.
	 */
	async function add(text: string) {
		const values = text
			.split(/[\n,]+/)
			.map((x) => x.trim())
			.filter(Boolean);
		const results = await Promise.allSettled(values.map((v) => normalize(v)));
		const have = new Set(items);
		const fresh: string[] = [];
		const bad: string[] = [];
		const why: string[] = [];
		results.forEach((r, i) => {
			if (r.status === 'rejected') {
				bad.push(values[i]);
				why.push(r.reason instanceof Error ? r.reason.message : String(r.reason));
			} else if (!have.has(r.value)) {
				have.add(r.value);
				fresh.push(r.value);
			}
		});
		if (fresh.length) items = [...items, ...fresh];
		input = bad.join(', ');
		refused = why;
	}

	function paste(e: ClipboardEvent) {
		const text = e.clipboardData?.getData('text') ?? '';
		if (!/[\n,]/.test(text)) return;
		e.preventDefault();
		add(text);
	}
</script>

<div class="list">
	<div class="label">{label} · {items.length}</div>
	{#each items as x (x)}
		<div class="item">
			<span>{x}</span>
			<button onclick={() => (items = items.filter((y) => y !== x))} title="убрать">×</button>
		</div>
	{/each}
	<input
		class="field mono"
		placeholder={hint}
		bind:value={input}
		onkeydown={(e) => e.key === 'Enter' && add(input)}
		onblur={() => input.trim() && add(input)}
		onpaste={paste}
	/>
	{#each refused as r (r)}
		<div class="bad refused">{r}</div>
	{/each}
</div>

<style>
	.list {
		min-width: 0;
	}

	.label {
		margin-bottom: 8px;
		font-size: 11px;
		color: var(--text-3);
	}

	.item {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		padding: 2px 0;
		font-family: var(--mono);
		font-size: 12px;
		color: var(--text-2);
	}

	.item span {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.item button {
		visibility: hidden;
		padding: 0 4px;
		color: var(--text-3);
	}

	.item:hover button {
		visibility: visible;
	}

	.item button:hover {
		color: var(--bad);
	}

	.field {
		margin-top: 6px;
	}

	.refused {
		margin-top: 4px;
		font-size: 11px;
	}
</style>
