<script lang="ts">
	import type { Snippet } from 'svelte';

	/**
	 * One setting on a console screen: what it is on the left, the control on
	 * the right. Rows stack edge to edge inside a flush Panel, one hairline
	 * between them.
	 */
	interface Props {
		label: string;
		/** A line under the label: why the knob is locked, what it costs. */
		hint?: string;
		children: Snippet;
	}

	let { label, hint, children }: Props = $props();
</script>

<div class="knob">
	<div>
		<div class="k-label">{label}</div>
		{#if hint}<div class="k-hint">{hint}</div>{/if}
	</div>
	{@render children()}
</div>

<style>
	.knob {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 20px;
		padding: 11px 16px;
		border-bottom: 1px solid var(--border-subtle);
	}

	.knob:last-child {
		border-bottom: none;
	}

	.k-label {
		font-size: 12px;
		color: var(--text);
	}

	.k-hint {
		font-size: 11px;
		color: var(--text-3);
		margin-top: 2px;
	}
</style>
