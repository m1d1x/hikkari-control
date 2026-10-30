<script lang="ts">
	import { fetchBoostStatus, setBoost, clearBoost, type BoostStatus } from '$lib/api/boost';

	interface Props {
		userId: string;
	}

	const { userId }: Props = $props();

	let status: BoostStatus = $state({ active: false });
	let loading = $state(false);
	let error: string | null = $state(null);

	let displayUserId = $state('');
	let pollHandle: ReturnType<typeof setInterval> | null = null;

	async function refresh() {
		if (!userId || !isUuid(userId)) {
			status = { active: false };
			return;
		}
		try {
			status = await fetchBoostStatus(userId);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	function isUuid(s: string): boolean {
		return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(s);
	}

	function startPolling() {
		stopPolling();
		// Refresh status every 30s while active so the countdown reflects reality
		// without keeping a websocket open.
		pollHandle = setInterval(refresh, 30_000);
	}

	function stopPolling() {
		if (pollHandle) {
			clearInterval(pollHandle);
			pollHandle = null;
		}
	}

	$effect(() => {
		if (userId !== displayUserId) {
			displayUserId = userId;
			error = null;
			refresh();
		}
		if (status.active) startPolling();
		else stopPolling();
		return () => stopPolling();
	});

	async function activate() {
		if (!userId || loading) return;
		loading = true;
		error = null;
		try {
			status = await setBoost(userId, 60);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	async function cancel() {
		if (!userId || loading) return;
		if (!confirm('Снять boost для этого пользователя?')) return;
		loading = true;
		error = null;
		try {
			status = await clearBoost(userId);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	function formatExpires(iso?: string): string {
		if (!iso) return '';
		const d = new Date(iso);
		if (isNaN(d.getTime())) return '';
		const pad = (n: number) => String(n).padStart(2, '0');
		return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
	}
</script>

{#if userId && isUuid(userId)}
	<div class="boost-box">
		{#if status.active}
			<span class="status-on" title="Активен boost — debug/trace логи попадают в CH несмотря на sampling">
				● Boost active until {formatExpires(status.expiresAt)}
			</span>
			<button class="btn btn-cancel" onclick={cancel} disabled={loading} title="Отменить boost">
				{loading ? '…' : 'Cancel'}
			</button>
		{:else}
			<button class="btn btn-on" onclick={activate} disabled={loading} title="Включить debug/trace для этого юзера на 1 час">
				{loading ? '…' : 'Boost logs (1h)'}
			</button>
		{/if}
		{#if error}
			<span class="err" title={error}>err</span>
		{/if}
	</div>
{/if}

<style>
	.boost-box {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 4px 8px;
		font-size: 11px;
		font-family: var(--font-mono);
	}

	.status-on {
		color: var(--status-healthy);
		font-weight: 500;
	}

	.btn {
		padding: 4px 10px;
		border-radius: 6px;
		border: 1px solid var(--border-subtle);
		background: var(--card-bg);
		color: var(--text-primary);
		font-size: 11px;
		font-family: inherit;
		cursor: pointer;
		transition: background var(--transition-fast);
	}
	.btn:hover:not(:disabled) {
		background: var(--surface-hover);
	}
	.btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.btn-on {
		border-color: color-mix(in oklab, var(--chart-2) 50%, transparent);
		color: var(--chart-2);
	}
	.btn-on:hover:not(:disabled) {
		background: color-mix(in oklab, var(--chart-2) 15%, transparent);
	}
	.btn-cancel {
		border-color: color-mix(in oklab, var(--status-warning) 50%, transparent);
		color: var(--status-warning);
	}

	.err {
		color: var(--status-critical);
		font-size: 10px;
		text-transform: uppercase;
	}
</style>
