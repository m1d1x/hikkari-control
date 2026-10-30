<script lang="ts">
	import { icons } from '$components/icons';

	interface Props {
		value?: string;
		placeholder?: string;
		/** CSS width; the default sizes to content, which is too narrow for a real query. */
		width?: string;
		onInput?: (value: string) => void;
	}

	let { value = $bindable(''), placeholder = 'Search...', width, onInput }: Props = $props();

	let timeout: ReturnType<typeof setTimeout>;

	function handleInput(e: Event) {
		const v = (e.target as HTMLInputElement).value;
		value = v;
		clearTimeout(timeout);
		timeout = setTimeout(() => onInput?.(v), 250);
	}
</script>

<div class="search" style:width>
	<span class="icon">{@html icons.search}</span>
	<input type="text" {value} {placeholder} oninput={handleInput} />
</div>

<style>
	.search {
		display: flex;
		align-items: center;
		gap: 8px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
		padding: 6px 12px;
		transition: border-color var(--transition-fast);
	}

	.search:focus-within {
		border-color: var(--primary-muted);
	}

	.icon {
		display: flex;
		color: var(--text-muted);
		flex-shrink: 0;
	}

	input {
		flex: 1;
		font-size: 13px;
		min-width: 0;
	}

	input::placeholder {
		color: var(--text-muted);
	}
</style>
