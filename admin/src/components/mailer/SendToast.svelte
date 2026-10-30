<script lang="ts">
	// Top-right toast for the «Send» action. Auto-dismisses after `duration`
	// (default 4s); user can hover to pause auto-dismiss, click X to close
	// immediately. Only one toast at a time on this page — kept simple.
	import { onMount } from 'svelte';

	interface Props {
		kind: 'success' | 'error';
		title: string;
		body?: string;
		duration?: number;
		onDismiss: () => void;
	}

	let { kind, title, body = '', duration = 4000, onDismiss }: Props = $props();

	let paused = $state(false);
	let timer: ReturnType<typeof setTimeout> | null = null;
	let elapsedAtPause = 0;
	let startTs = 0;

	function start(ms: number) {
		startTs = Date.now();
		timer = setTimeout(onDismiss, ms);
	}

	onMount(() => {
		start(duration);
		return () => {
			if (timer) clearTimeout(timer);
		};
	});

	function pause() {
		if (paused || !timer) return;
		paused = true;
		elapsedAtPause = Date.now() - startTs;
		clearTimeout(timer);
		timer = null;
	}

	function resume() {
		if (!paused) return;
		paused = false;
		const remaining = Math.max(500, duration - elapsedAtPause);
		start(remaining);
	}
</script>

<div
	class="toast {kind}"
	role="status"
	aria-live="polite"
	onmouseenter={pause}
	onmouseleave={resume}
>
	<div class="icon" aria-hidden="true">
		{#if kind === 'success'}
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"
				><path d="M5 13l4 4L19 7" stroke-linecap="round" stroke-linejoin="round" /></svg
			>
		{:else}
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"
				><path d="M6 6l12 12M18 6L6 18" stroke-linecap="round" stroke-linejoin="round" /></svg
			>
		{/if}
	</div>
	<div class="text">
		<div class="title">{title}</div>
		{#if body}
			<div class="body">{body}</div>
		{/if}
	</div>
	<button type="button" class="close" aria-label="Закрыть" onclick={onDismiss}>×</button>
</div>

<style>
	.toast {
		position: fixed;
		top: 20px;
		right: 20px;
		display: flex;
		gap: 12px;
		align-items: flex-start;
		min-width: 280px;
		max-width: 420px;
		padding: 14px 16px;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 10px;
		box-shadow: 0 8px 24px rgba(0, 0, 0, 0.3);
		z-index: 1000;
		animation: slide-in 0.25s ease-out;
	}

	.toast.success {
		border-left: 3px solid var(--status-healthy);
	}

	.toast.error {
		border-left: 3px solid var(--status-critical);
	}

	.icon {
		flex-shrink: 0;
		width: 20px;
		height: 20px;
		margin-top: 1px;
	}

	.toast.success .icon {
		color: var(--status-healthy);
	}

	.toast.error .icon {
		color: var(--status-critical);
	}

	.icon svg {
		width: 100%;
		height: 100%;
	}

	.text {
		flex: 1;
		min-width: 0;
	}

	.title {
		font-size: 14px;
		font-weight: 600;
		color: var(--text-primary);
		margin-bottom: 2px;
	}

	.body {
		font-size: 12px;
		color: var(--text-secondary);
		word-break: break-word;
	}

	.close {
		flex-shrink: 0;
		width: 24px;
		height: 24px;
		font-size: 22px;
		line-height: 1;
		color: var(--text-muted);
		background: transparent;
		border: none;
		cursor: pointer;
		padding: 0;
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.close:hover {
		color: var(--text-primary);
	}

	@keyframes slide-in {
		from {
			opacity: 0;
			transform: translateY(-8px);
		}
		to {
			opacity: 1;
			transform: translateY(0);
		}
	}
</style>
