<script lang="ts">
	/**
	 * The service's own brand mark, served from `static/services/<id>.png`.
	 *
	 * The ids are the catalog's icon ids (`SERVICE_ICONS`), and the files are the
	 * same real logos the client ships — copied from the client's
	 * `app/assets/services`, where `SOURCES.md` records each one's origin.
	 * A group may carry an id we ship no file for (hand-typed): show the bare id
	 * tag instead of a broken image.
	 */
	import { SERVICE_ICONS } from '$lib/product/presets';

	interface Props {
		id: string;
		size?: number;
	}
	let { id, size = 16 }: Props = $props();

	const known = new Set(SERVICE_ICONS.map((x) => x.id));
</script>

{#if known.has(id)}
	<img
		class="service-logo"
		src={`/services/${id}.png`}
		alt=""
		width={size}
		height={size}
		loading="lazy"
		decoding="async"
	/>
{:else if id}
	<span class="icon-tag mono">{id}</span>
{/if}

<style>
	.service-logo {
		display: inline-block;
		vertical-align: middle;
		border-radius: 3px;
		flex: none;
	}
	.icon-tag {
		font-size: 10px;
		line-height: 1;
		padding: 1px 4px;
		border-radius: 3px;
		background: rgba(255, 255, 255, 0.08);
		color: var(--dim, #888);
		border: 1px solid rgba(255, 255, 255, 0.1);
	}
</style>
