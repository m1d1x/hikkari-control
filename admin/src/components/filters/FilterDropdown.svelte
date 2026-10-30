<script lang="ts">
	interface Option {
		value: string;
		label: string;
	}

	interface Props {
		options: Option[];
		value?: string;
		placeholder?: string;
		onChange?: (value: string) => void;
	}

	let { options, value = $bindable(''), placeholder = 'Select...', onChange }: Props = $props();

	function handleChange(e: Event) {
		const v = (e.target as HTMLSelectElement).value;
		value = v;
		onChange?.(v);
	}
</script>

<select class="dropdown" {value} onchange={handleChange}>
	{#if placeholder}
		<option value="">{placeholder}</option>
	{/if}
	{#each options as opt}
		<option value={opt.value}>{opt.label}</option>
	{/each}
</select>

<style>
	.dropdown {
		font-size: 13px;
		padding: 6px 28px 6px 12px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
		color: var(--text-primary);
		cursor: pointer;
		appearance: none;
		background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='%23888' stroke-width='2'%3E%3Cpath d='M6 9l6 6 6-6'/%3E%3C/svg%3E");
		background-repeat: no-repeat;
		background-position: right 8px center;
		transition: border-color var(--transition-fast);
	}

	.dropdown:hover {
		border-color: var(--border);
	}

	.dropdown:focus {
		border-color: var(--primary-muted);
	}
</style>
