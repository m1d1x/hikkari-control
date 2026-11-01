<script lang="ts">
	import type { BlueGreenStatus, BlueGreenNode, BlueGreenSwitchEvent } from '$lib/types/system';
	import { formatRelativeTime, formatUptimeSeconds } from '$lib/utils/format';

	interface Props {
		status: BlueGreenStatus | null;
	}

	let { status }: Props = $props();

	// Pick the blue/green node to highlight at the top. There's only ever one
	// prod node participating in blue/green; if more than one is wired up later
	// we render the first that reports a non-empty color. Other nodes (e.g.
	// sfu-eu-1) appear in the secondary list below.
	let primary = $derived<BlueGreenNode | null>(
		status?.nodes?.find((n) => n.color === 'blue' || n.color === 'green') ?? null,
	);

	let otherNodes = $derived<BlueGreenNode[]>(
		(status?.nodes ?? []).filter((n) => n !== primary),
	);

	// Switch transition flag: true for ~10s after the most recent event so the
	// indicator can pulse / show a progress bar for the just-completed flip.
	const SWITCH_FLASH_MS = 10_000;
	let now = $state(Date.now());
	$effect(() => {
		const t = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(t);
	});

	let lastSwitch = $derived<BlueGreenSwitchEvent | null>(
		status?.history && status.history.length > 0
			? status.history[status.history.length - 1]
			: null,
	);

	let switchAgeMs = $derived(
		lastSwitch ? now - new Date(lastSwitch.at).getTime() : Infinity,
	);
	let inFlash = $derived(switchAgeMs < SWITCH_FLASH_MS);
	let flashProgress = $derived(
		inFlash ? Math.min(switchAgeMs / SWITCH_FLASH_MS, 1) : 1,
	);

	// Visual progress bar fills from 0 → 100% over the first 5 minutes since
	// the switch, then sits steady. Gives a quick "fresh deploy" affordance
	// without making a permanent shouty UI.
	const FRESH_WINDOW_MS = 5 * 60 * 1000;
	let freshProgress = $derived(() => {
		if (!primary?.uptimeSeconds) return 1;
		const ms = primary.uptimeSeconds * 1000;
		return Math.min(ms / FRESH_WINDOW_MS, 1);
	});

	function shortSha(sha: string | undefined): string {
		if (!sha) return '—';
		return sha.length > 7 ? sha.slice(0, 7) : sha;
	}

	function shortTag(tag: string | undefined): string {
		if (!tag) return '—';
		// Drop registry prefix for readability: ghcr.io/m1d1x/hikkari:foo → :foo
		const colon = tag.lastIndexOf(':');
		return colon === -1 ? tag : tag.slice(colon + 1);
	}

	function pillClass(color: string, active: string): string {
		if (color === active) return 'pill pill-active pill-' + color;
		return 'pill pill-idle pill-' + color;
	}

	let recentHistory = $derived<BlueGreenSwitchEvent[]>(
		(status?.history ?? []).slice(-5).reverse(),
	);
</script>

<section class="bg-card" class:bg-card-flash={inFlash}>
	<header class="bg-head">
		<div class="bg-title">
			<span
				class="bg-dot"
				class:bg-dot-blue={primary?.ok && primary?.color === 'blue'}
				class:bg-dot-green={primary?.ok && primary?.color === 'green'}
				class:bg-dot-bad={primary && !primary.ok}
			></span>
			<span class="bg-label">Blue / Green</span>
			{#if primary}
				<span class="bg-node mono">{primary.nodeId || primary.name}</span>
			{/if}
		</div>
		{#if status}
			<span class="bg-updated mono">
				probed {formatRelativeTime(status.generatedAt)}
			</span>
		{/if}
	</header>

	<div class="bg-pills">
		<div class={pillClass('blue', primary?.color ?? '')}>
			<span class="pill-name">BLUE</span>
			<span class="pill-port mono">:3300</span>
			{#if primary?.color === 'blue'}
				<span class="pill-live">LIVE</span>
			{/if}
		</div>

		<div
			class="pill-arrow"
			class:pill-arrow-left={primary?.color === 'blue'}
			class:pill-arrow-flash={inFlash}
			aria-hidden="true"
		>
			<svg viewBox="0 0 32 16" width="36" height="18">
				<path
					class="arrow-shaft"
					d="M2 8 L26 8 M20 2 L26 8 L20 14"
					stroke="currentColor"
					stroke-width="2"
					fill="none"
					stroke-linecap="round"
					stroke-linejoin="round"
				/>
			</svg>
		</div>

		<div class={pillClass('green', primary?.color ?? '')}>
			<span class="pill-name">GREEN</span>
			<span class="pill-port mono">:3301</span>
			{#if primary?.color === 'green'}
				<span class="pill-live">LIVE</span>
			{/if}
		</div>
	</div>

	{#if primary}
		<div class="bg-meta">
			<div class="meta-item">
				<span class="meta-key">Image</span>
				<span class="meta-val mono" title={primary.imageTag ?? ''}>{shortTag(primary.imageTag)}</span>
			</div>
			<div class="meta-item">
				<span class="meta-key">SHA</span>
				<span class="meta-val mono">{shortSha(primary.releaseSha)}</span>
			</div>
			<div class="meta-item">
				<span class="meta-key">Uptime</span>
				<span class="meta-val mono">{primary.uptimeSeconds ? formatUptimeSeconds(primary.uptimeSeconds) : '—'}</span>
			</div>
			<div class="meta-item">
				<span class="meta-key">Last switch</span>
				<span class="meta-val mono">{lastSwitch ? formatRelativeTime(lastSwitch.at) : 'never'}</span>
			</div>
		</div>

		<div class="bg-progress" aria-label="time since switch">
			<div class="bg-progress-track">
				<div
					class="bg-progress-fill"
					class:bg-progress-flash={inFlash}
					style="width: {(inFlash ? flashProgress : freshProgress()) * 100}%; background: {primary.color === 'blue' ? 'var(--bg-blue)' : 'var(--bg-green)'}"
				></div>
			</div>
			<span class="bg-progress-label mono">
				{#if inFlash}
					switching… {Math.round((1 - flashProgress) * 10)}s
				{:else if primary.uptimeSeconds && primary.uptimeSeconds < 300}
					fresh deploy ({primary.uptimeSeconds}s)
				{:else}
					stable
				{/if}
			</span>
		</div>
	{:else}
		<div class="bg-empty mono">No blue/green-aware node reachable yet.</div>
	{/if}

	{#if otherNodes.length > 0}
		<div class="bg-others">
			{#each otherNodes as node}
				<div class="other">
					<span class="other-dot" class:other-dot-ok={node.ok} class:other-dot-bad={!node.ok}></span>
					<span class="other-name mono">{node.nodeId || node.name}</span>
					<span class="other-sha mono">{shortSha(node.releaseSha)}</span>
					{#if node.error}
						<span class="other-err">{node.error}</span>
					{/if}
				</div>
			{/each}
		</div>
	{/if}

	{#if recentHistory.length > 0}
		<details class="bg-history">
			<summary>Switch history ({status?.history?.length ?? 0})</summary>
			<ul class="history-list">
				{#each recentHistory as ev}
					<li class="history-item">
						<span class="history-arrow">
							<span class="history-from history-{ev.from}">{ev.from}</span>
							<span aria-hidden="true">→</span>
							<span class="history-to history-{ev.to}">{ev.to}</span>
						</span>
						<span class="history-sha mono">{shortSha(ev.fromSha)} → {shortSha(ev.toSha)}</span>
						<span class="history-when mono">{formatRelativeTime(ev.at)}</span>
					</li>
				{/each}
			</ul>
		</details>
	{/if}
</section>

<style>
	:root {
		--bg-blue: #3a82ff;
		--bg-green: #2bd07b;
	}

	.bg-card {
		--card-accent: var(--border);
		position: relative;
		display: flex;
		flex-direction: column;
		gap: 14px;
		padding: 18px 20px;
		margin-bottom: 24px;
		background: var(--bg-raised);
		border: 1px solid var(--border);
		border-radius: 12px;
		box-shadow: 0 1px 0 rgba(0, 0, 0, 0.04);
		transition: border-color 0.4s ease, box-shadow 0.4s ease;
	}

	.bg-card-flash {
		border-color: var(--bg-blue);
		box-shadow: 0 0 0 4px rgba(58, 130, 255, 0.15);
		animation: bg-pulse 1.6s ease-in-out infinite;
	}

	@keyframes bg-pulse {
		0%,
		100% {
			box-shadow: 0 0 0 4px rgba(58, 130, 255, 0.15);
		}
		50% {
			box-shadow: 0 0 0 8px rgba(58, 130, 255, 0.05);
		}
	}

	.bg-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
	}

	.bg-title {
		display: flex;
		align-items: center;
		gap: 10px;
	}

	.bg-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--text-muted);
	}
	.bg-dot-blue {
		background: var(--bg-blue);
		box-shadow: 0 0 0 4px rgba(58, 130, 255, 0.18);
	}
	.bg-dot-green {
		background: var(--bg-green);
		box-shadow: 0 0 0 4px rgba(43, 208, 123, 0.18);
	}
	.bg-dot-bad {
		background: var(--status-critical, #ef4444);
		box-shadow: 0 0 0 4px rgba(239, 68, 68, 0.18);
	}

	.bg-label {
		font-size: 13px;
		font-weight: 600;
		letter-spacing: 0.04em;
		color: var(--text-secondary);
		text-transform: uppercase;
	}

	.bg-node {
		font-size: 12px;
		color: var(--text-muted);
		padding: 2px 8px;
		border-radius: 4px;
		background: var(--bg);
	}

	.bg-updated {
		font-size: 11px;
		color: var(--text-muted);
	}

	.bg-pills {
		display: flex;
		align-items: center;
		gap: 12px;
	}

	.pill {
		flex: 1;
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 14px 18px;
		border-radius: 10px;
		border: 1px solid var(--border);
		background: var(--bg);
		font-weight: 700;
		letter-spacing: 0.06em;
		transition: all 0.3s ease;
	}

	.pill-idle {
		opacity: 0.42;
		filter: grayscale(0.4);
	}

	.pill-active.pill-blue {
		color: #fff;
		background: linear-gradient(135deg, #2870ff, #4791ff);
		border-color: #4791ff;
	}
	.pill-active.pill-green {
		color: #fff;
		background: linear-gradient(135deg, #1ea860, #2bd07b);
		border-color: #2bd07b;
	}

	.pill-name {
		font-size: 16px;
	}
	.pill-port {
		font-size: 12px;
		font-weight: 500;
		opacity: 0.85;
	}

	.pill-live {
		margin-left: auto;
		font-size: 10px;
		font-weight: 800;
		padding: 3px 8px;
		border-radius: 4px;
		background: rgba(255, 255, 255, 0.18);
		letter-spacing: 0.1em;
	}

	.pill-arrow {
		color: var(--text-muted);
		display: flex;
		align-items: center;
	}

	.pill-arrow svg {
		transition: transform 0.6s cubic-bezier(0.65, 0, 0.35, 1);
	}

	.pill-arrow-left svg {
		transform: rotate(180deg);
	}

	.pill-arrow-flash {
		color: var(--text-primary);
	}

	.pill-arrow-flash .arrow-shaft {
		stroke-dasharray: 6 6;
		animation: arrow-flow 0.6s linear infinite;
	}

	.pill-arrow-flash svg {
		animation: arrow-pulse 1s ease-in-out infinite;
	}

	@keyframes arrow-flow {
		from {
			stroke-dashoffset: 0;
		}
		to {
			stroke-dashoffset: -12;
		}
	}

	@keyframes arrow-pulse {
		0%,
		100% {
			opacity: 0.55;
			transform: scale(1) var(--arrow-flip, rotate(0deg));
		}
		50% {
			opacity: 1;
			transform: scale(1.15) var(--arrow-flip, rotate(0deg));
		}
	}

	.pill-arrow-left.pill-arrow-flash {
		--arrow-flip: rotate(180deg);
	}

	.bg-meta {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 12px;
	}

	.meta-item {
		display: flex;
		flex-direction: column;
		gap: 4px;
		padding: 10px 14px;
		background: var(--bg);
		border-radius: 8px;
		border: 1px solid var(--border-subtle);
	}

	.meta-key {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--text-muted);
	}
	.meta-val {
		font-size: 13px;
		color: var(--text-primary);
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.bg-progress {
		display: flex;
		align-items: center;
		gap: 10px;
	}

	.bg-progress-track {
		flex: 1;
		height: 6px;
		background: var(--border-subtle);
		border-radius: 3px;
		overflow: hidden;
	}

	.bg-progress-fill {
		height: 100%;
		border-radius: 3px;
		transition: width 0.6s ease;
	}

	.bg-progress-flash {
		animation: bg-flash-stripes 1s linear infinite;
		background-image: linear-gradient(
			90deg,
			rgba(255, 255, 255, 0) 0%,
			rgba(255, 255, 255, 0.35) 50%,
			rgba(255, 255, 255, 0) 100%
		);
		background-size: 60px 100%;
		background-repeat: repeat-x;
	}

	@keyframes bg-flash-stripes {
		from {
			background-position: 0 0;
		}
		to {
			background-position: 60px 0;
		}
	}

	.bg-progress-label {
		font-size: 11px;
		color: var(--text-muted);
		min-width: 110px;
		text-align: right;
	}

	.bg-empty {
		font-size: 12px;
		color: var(--text-muted);
		padding: 8px 0;
	}

	.bg-others {
		display: flex;
		flex-wrap: wrap;
		gap: 14px;
		padding-top: 4px;
		border-top: 1px dashed var(--border-subtle);
		margin-top: 4px;
	}

	.other {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		padding: 6px 10px;
		background: var(--bg);
		border-radius: 6px;
	}

	.other-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--text-muted);
	}
	.other-dot-ok {
		background: var(--status-healthy);
	}
	.other-dot-bad {
		background: var(--status-critical, #ef4444);
	}

	.other-name {
		color: var(--text-primary);
		font-weight: 600;
	}
	.other-sha {
		color: var(--text-muted);
	}
	.other-err {
		color: var(--status-critical, #ef4444);
		font-size: 11px;
	}

	.bg-history {
		font-size: 12px;
		color: var(--text-secondary);
	}

	.bg-history summary {
		cursor: pointer;
		padding: 6px 0;
		color: var(--text-muted);
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		user-select: none;
	}

	.bg-history summary:hover {
		color: var(--text-secondary);
	}

	.history-list {
		list-style: none;
		padding: 0;
		margin: 8px 0 0;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.history-item {
		display: grid;
		grid-template-columns: 1fr 1.4fr auto;
		align-items: center;
		gap: 12px;
		padding: 6px 12px;
		background: var(--bg);
		border-radius: 6px;
	}

	.history-arrow {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.history-from,
	.history-to {
		font-size: 11px;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		padding: 2px 8px;
		border-radius: 4px;
	}

	.history-blue {
		background: rgba(58, 130, 255, 0.18);
		color: #4791ff;
	}
	.history-green {
		background: rgba(43, 208, 123, 0.18);
		color: #2bd07b;
	}

	.history-sha {
		font-size: 11px;
		color: var(--text-muted);
	}

	.history-when {
		font-size: 11px;
		color: var(--text-muted);
	}

	@media (max-width: 800px) {
		.bg-meta {
			grid-template-columns: repeat(2, 1fr);
		}
		.bg-pills {
			flex-direction: column;
			align-items: stretch;
		}
		.pill-arrow {
			transform: rotate(90deg);
			align-self: center;
		}
	}
</style>
