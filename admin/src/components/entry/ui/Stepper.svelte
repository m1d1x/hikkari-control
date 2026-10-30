<script lang="ts">
	/**
	 * − value + for a numeric knob. The value shown is the caller's: a stepper
	 * on seconds may well show minutes. Each press is one save, so the caller
	 * decides the step and the bounds.
	 */
	interface Props {
		value: string;
		unit?: string;
		/** Below the lower bound: − is disabled. */
		atMin: boolean;
		/** At the upper bound: + is disabled. */
		atMax: boolean;
		/** A save is in flight: both buttons wait. */
		busy?: boolean;
		ondown: () => void;
		onup: () => void;
	}

	let { value, unit, atMin, atMax, busy = false, ondown, onup }: Props = $props();
</script>

<div class="stepper">
	<button type="button" disabled={busy || atMin} onclick={ondown}>−</button>
	<span class="val m"
		>{value}{#if unit}&nbsp;<span class="u">{unit}</span>{/if}</span
	>
	<button type="button" disabled={busy || atMax} onclick={onup}>+</button>
</div>

<style>
	.stepper {
		display: flex;
		align-items: center;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--inset);
		overflow: hidden;
		flex-shrink: 0;
	}

	.stepper button {
		padding: 4px 10px;
		color: var(--text-2);
		background: none;
		border: none;
		cursor: pointer;
		font-size: 14px;
	}

	.stepper button:hover:not(:disabled) {
		background: var(--surface);
		color: var(--text);
	}

	.stepper button:disabled {
		opacity: 0.4;
		cursor: default;
	}

	.val {
		padding: 4px 12px;
		font-size: 12px;
		font-weight: 600;
		border-left: 1px solid var(--border);
		border-right: 1px solid var(--border);
		min-width: 64px;
		text-align: center;
	}

	.u {
		font-size: 10px;
		font-weight: 400;
		color: var(--text-3);
	}
</style>
