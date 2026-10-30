<script lang="ts">
	import type { Snippet } from 'svelte';

	/**
	 * A section of a console screen: one card, one lowercase title, and
	 * optionally controls that belong to this card alone pinned to the right
	 * of its head. A card with nothing in its head draws no head at all — an
	 * empty strip above the content is the "lишний заголовок" the reference
	 * screens got rid of.
	 */
	interface Props {
		title?: string;
		/** A short fact about the card's scope, e.g. "214 за всё время". */
		sub?: string;
		/** Controls for this card: filters, toggles, an action button. */
		controls?: Snippet;
		/** Removes the card's padding, for a table that goes edge to edge. */
		flush?: boolean;
		children: Snippet;
	}

	let { title, sub, controls, flush = false, children }: Props = $props();
</script>

<section class="panel">
	{#if title || controls}
		<div class="panel-head">
			{#if title}<h2>{title}</h2>{/if}
			{#if sub}<span class="sub">{sub}</span>{/if}
			{#if controls}
				<div class="controls">{@render controls()}</div>
			{/if}
		</div>
	{/if}
	<div class="panel-body" class:flush>
		{@render children()}
	</div>
</section>

<style>
	.panel {
		background: var(--card-bg);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		margin-bottom: 16px;
	}

	.panel-head {
		display: flex;
		align-items: baseline;
		gap: 10px;
		padding: 13px 16px;
		border-bottom: 1px solid var(--border-subtle);
	}

	.panel-head h2 {
		margin: 0;
		font-size: 13px;
		font-weight: 600;
		color: var(--text);
	}

	.sub {
		font-size: 11px;
		color: var(--text-3);
	}

	.controls {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.panel-body {
		padding: 16px;
	}

	.panel-body.flush {
		padding: 0;
	}
</style>
