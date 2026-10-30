<script lang="ts">
	interface Props {
		width?: string;
		height?: string;
		rounded?: boolean;
	}

	let { width = '100%', height = '16px', rounded = false }: Props = $props();
</script>

<div class="skeleton" class:rounded style="width: {width}; height: {height}"></div>

<style>
	.skeleton {
		background: var(--surface-hover);
		border-radius: 4px;
		animation: pulse 1.5s ease-in-out infinite;
	}

	.rounded {
		border-radius: 50%;
	}

	@keyframes pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.5;
		}
	}
</style>
