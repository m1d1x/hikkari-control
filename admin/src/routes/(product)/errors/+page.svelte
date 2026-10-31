<script lang="ts">
	import WinChips from '$components/product/WinChips.svelte';
	import { fetchProductErrors } from '$lib/api/product';
	import { num, ago } from '$lib/product/format';
	import { Windowed } from '$lib/product/windowed.svelte';

	// Groups of the same scrubbed failure (code, component, fingerprint),
	// counted by the hub from client telemetry. The stack block is part of the
	// approved design; the client does not send stacks yet.
	const e = new Windowed('24h', fetchProductErrors);
	let os = $state<string | null>(null);
	let query = $state('');
	let open = $state<string | null>(null);

	const oses = $derived([...new Set((e.data?.groups ?? []).flatMap((g) => g.oses))].sort());
	const list = $derived(
		(e.data?.groups ?? []).filter(
			(g) =>
				(!os || g.oses.includes(os)) &&
				(g.message + g.code + g.component).toLowerCase().includes(query.trim().toLowerCase()),
		),
	);
	const shown = $derived(list.reduce((s, g) => s + g.occurrences, 0));
	/** first seen inside the window's last day: a fresh breakage, not a known one */
	const isNew = (t: number) => Date.now() - t < 24 * 3600_000;
</script>

<div class="head">
	<h1>ошибки</h1>
	{#if oses.length > 1}
		<div class="chips">
			<button class="chip" class:on={!os} onclick={() => (os = null)}>все</button>
			{#each oses as o (o)}
				<button class="chip" class:on={os === o} onclick={() => (os = o)}>{o}</button>
			{/each}
		</div>
	{/if}
	<WinChips bind:value={e.win} />
</div>

{#if e.error}
	<p class="warn">не загрузилось: {e.error}</p>
{/if}

{#if e.data}
	<section class="card">
		<div class="top">
			<input class="field" placeholder="поиск по тексту, коду или модулю" bind:value={query} />
			<span class="dim">
				{list.length} групп · <b>{num(shown)}</b> раз
				{#if !os && !query.trim()}· <b>{num(e.data.installs)}</b> установок задето{/if}
				{#if e.loading}· обновляю…{/if}
			</span>
		</div>

		{#if e.data.groups.length === 0}
			<p class="empty">за окно ошибок не пришло</p>
		{:else if list.length === 0}
			<p class="empty">ничего не нашлось</p>
		{:else}
			<table class="rows">
				<thead>
					<tr><th class="r">раз</th><th>ошибка</th><th class="r">установок</th><th class="r">последняя</th></tr>
				</thead>
				<tbody>
					{#each list as g (g.id)}
						<tr class="err" onclick={() => (open = open === g.id ? null : g.id)}>
							<td class="r"><b>{num(g.occurrences)}</b></td>
							<td>
								<div class="msg" class:clip={open !== g.id}>{g.message || g.code}</div>
								<div class="dim">
									{#if isNew(g.firstSeen)}<span class="warn">новая</span> ·{/if}
									{g.code} · {g.component} · {g.oses.join(', ')} · {g.versions.slice(0, 3).join(', ')}{g.versions.length > 3 ? ' …' : ''}
								</div>
								{#if open === g.id}
									<pre>{g.message}</pre>
									<div class="full">
										впервые {ago(g.firstSeen)} назад · версии: {g.versions.join(', ')}
										<div class="dim">стек: клиент пока не передаёт</div>
									</div>
								{/if}
							</td>
							<td class="r"><b>{num(g.installs)}</b></td>
							<td class="r dim">{ago(g.lastSeen)}</td>
						</tr>
					{/each}
				</tbody>
			</table>
			{#if e.data.truncated}
				<p class="empty">показаны {e.data.groups.length} самых частых групп</p>
			{/if}
		{/if}
	</section>
{:else if e.loading}
	<p class="empty">загружаю…</p>
{/if}

<style>
	.top {
		display: flex;
		align-items: center;
		gap: 16px;
		margin-bottom: 14px;
	}

	.top .field {
		max-width: 320px;
	}

	.top span {
		margin-left: auto;
		font-size: 11px;
	}

	.err {
		cursor: pointer;
	}

	.err:hover td {
		background: oklch(0.21 0 0);
	}

	.r {
		width: 1%;
		white-space: nowrap;
	}

	.msg {
		font-family: var(--mono);
		font-size: 12px;
	}

	.msg.clip {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 720px;
	}

	.rows .dim {
		font-size: 11px;
	}

	pre,
	.full {
		margin-top: 8px;
		padding: 10px 12px;
		background: var(--inset);
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
		font-family: var(--mono);
		font-size: 11px;
		color: var(--text-2);
		white-space: pre-wrap;
	}
</style>
