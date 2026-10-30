<script lang="ts">
	// The anti-blocking console's six screens share one set of design tokens
	// (roadmap 42 П9.9 §6а): loading them here keeps every screen from
	// re-declaring the palette it draws with.
	import '$lib/styles/entry.css';

	let { children } = $props();
</script>

{@render children()}
