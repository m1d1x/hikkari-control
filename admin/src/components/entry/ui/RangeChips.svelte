<script lang="ts" generics="T extends string">
	/**
	 * The window picker. Every screen in the section shows one, in the same
	 * place and with the same labels, because the figures below it only mean
	 * something together with the window they were measured over.
	 */
	interface Props {
		options: readonly { key: T; label: string }[];
		value: T;
		onpick: (v: T) => void;
	}

	let { options, value, onpick }: Props = $props();
</script>

<div class="ranges">
	{#each options as o (o.key)}
		<button class="chip" class:on={value === o.key} onclick={() => onpick(o.key)}>{o.label}</button>
	{/each}
</div>

<style>
	.ranges {
		display: flex;
		gap: 4px;
	}

	.chip {
		padding: 4px 11px;
		border-radius: 6px;
		font-size: 11px;
		color: var(--text-2);
		border: 1px solid var(--border);
		white-space: nowrap;
		cursor: pointer;
		background: none;
		transition: background 120ms ease;
	}

	.chip:hover {
		background: var(--surface);
	}

	.chip.on {
		color: var(--text);
		background: var(--primary-subtle);
		border-color: oklch(0.75 0.14 350 / 0.35);
	}
</style>
