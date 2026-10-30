<script lang="ts">
	import { PROTO } from '$lib/api/entry';

	interface Props {
		proto: string;
		port?: number;
	}

	let { proto, port }: Props = $props();

	// An unknown protocol shows its raw wire name rather than nothing: a newer
	// signer may hand out a kind this console was not taught yet.
	const info = $derived(PROTO[proto] ?? { tag: proto || '?', tone: 'other' });
</script>

<span class="proto {info.tone}">{info.tag}{#if port}<span class="port">:{port}</span>{/if}</span>

<style>
	.proto {
		display: inline-flex;
		align-items: baseline;
		gap: 1px;
		padding: 1px 7px;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 11px;
		border-radius: 5px;
		border: 1px solid currentColor;
		white-space: nowrap;
	}

	.port {
		opacity: 0.7;
	}

	.reality {
		color: #6192d4;
	}

	.hysteria {
		color: #c9a94e;
	}

	.amnezia {
		color: #ab5abf;
	}

	.xhttp {
		color: #4fa88a;
	}

	.other {
		color: var(--text-muted);
	}
</style>
