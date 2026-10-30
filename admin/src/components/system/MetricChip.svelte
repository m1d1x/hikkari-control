<script lang="ts">
	interface Props {
		label: string;
		value: string;
		max?: string;
		level?: 'ok' | 'warning' | 'critical';
	}

	let { label, value, max, level = 'ok' }: Props = $props();

	let pct = $derived(() => {
		if (!max) return 0;
		const cur = parseFloat(value);
		const mx = parseFloat(max);
		if (!mx) return 0;
		return Math.min((cur / mx) * 100, 100);
	});

	function levelColor(l: string): string {
		if (l === 'critical') return 'var(--status-critical)';
		if (l === 'warning') return 'var(--status-warning)';
		return 'var(--text-primary)';
	}
</script>

<div class="chip">
	<span class="chip-key">{label}</span>
	<span class="chip-val mono" style="color: {levelColor(level)}">
		{value}{#if max}<span class="chip-max"> / {max}</span>{/if}
	</span>
	{#if max && pct() > 0}
		<div class="chip-bar">
			<div
				class="chip-bar-fill"
				style="width: {pct()}%; background: {levelColor(level)}"
			></div>
		</div>
	{/if}
</div>

<style>
	.chip {
		display: flex;
		flex-direction: column;
		gap: 5px;
		padding: 8px 14px;
		background: var(--bg-raised);
		border-radius: 6px;
		font-size: 14px;
		min-width: 100px;
	}

	.chip-key {
		color: var(--text-muted);
		font-size: 11px;
	}

	.chip-val {
		font-weight: 600;
	}

	.chip-max {
		color: var(--text-muted);
		font-weight: 400;
	}

	.chip-bar {
		height: 3px;
		background: var(--border-subtle);
		border-radius: 2px;
		overflow: hidden;
	}

	.chip-bar-fill {
		height: 100%;
		border-radius: 1px;
		transition: width 0.3s ease;
	}
</style>
