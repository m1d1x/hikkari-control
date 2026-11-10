<script lang="ts">
	import { onMount } from 'svelte';
	import { describeApiError } from '$lib/api/client';
	import {
		fetchTesterInstalls,
		fetchTesterRelays,
		renameTester,
		type TesterInstall,
		type TesterRelays,
	} from '$lib/api/support';
	import { ago } from '$lib/product/format';

	// Testers are the hub's users today: installs of the client in tester mode,
	// not subscriptions sold or issued, so they come from the tester stream.
	let { installs = $bindable([]) }: { installs?: TesterInstall[] } = $props();

	let rounds = $state<Record<string, TesterRelays>>({});
	let error = $state('');

	async function load() {
		try {
			const [list, relays] = await Promise.all([fetchTesterInstalls(), fetchTesterRelays('24h')]);
			installs = [...list.installs].sort((a, b) => Number(b.online) - Number(a.online) || b.last_seen.localeCompare(a.last_seen));
			rounds = Object.fromEntries(relays.installs.map((r) => [r.install_id, r]));
			error = '';
		} catch (e) {
			error = describeApiError(e);
		}
	}

	// Rename in place: the name is how the owner says who to look at
	// («посмотри у Леши»), and the agent finds the logs by it.
	let editing = $state<string | null>(null);
	let draftAlias = $state('');

	function startRename(i: TesterInstall) {
		editing = i.install_id;
		draftAlias = i.alias;
	}

	async function commitRename(i: TesterInstall) {
		const name = draftAlias.trim();
		editing = null;
		if (!name || name === i.alias) return;
		try {
			await renameTester(i.install_id, name, i.note);
			await load();
		} catch (e) {
			error = describeApiError(e);
		}
	}

	const alive = (id: string) => rounds[id]?.relays.filter((r) => r.up).length ?? null;

	onMount(load);
</script>

<section class="card">
	<div class="top">
		<h2>тестеры</h2>
	</div>
	{#if error}<p class="bad">{error}</p>{/if}
	{#if installs.length === 0}
		<p class="empty">ни один тестер ещё не выходил на связь</p>
	{:else}
		<table class="rows testers">
			<tbody>
				{#each installs as i (i.install_id)}
					<tr>
						<td>
							{#if editing === i.install_id}
								<input
									class="field rename"
									bind:value={draftAlias}
									onblur={() => commitRename(i)}
									onkeydown={(e) => {
										if (e.key === 'Enter') e.currentTarget.blur();
										if (e.key === 'Escape') {
											draftAlias = i.alias;
											editing = null;
										}
									}}
									{@attach (el: HTMLInputElement) => el.select()}
								/>
							{:else}
								<i class="status" class:on={i.online}></i><button class="name" title="переименовать" onclick={() => startRename(i)}>{i.alias}</button>
							{/if}
						</td>
						<td class="mono dim">{i.platform} {i.version}</td>
						<td class="mono">
							{#if alive(i.install_id) === null}-{:else}{alive(i.install_id)} из {rounds[i.install_id].relays.length} профилей{/if}
						</td>
						<td class="r mono dim">{ago(new Date(i.last_seen).getTime())}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</section>

<style>
	.top {
		display: flex;
		align-items: center;
		gap: 16px;
		margin-bottom: 14px;
	}

	.top h2 {
		margin: 0;
	}

	.testers {
		table-layout: fixed;
	}

	.testers td:nth-child(1) {
		width: 34%;
	}

	.testers td:nth-child(2) {
		width: 26%;
	}

	.testers td:nth-child(3) {
		width: 28%;
	}

	.testers td:nth-child(4) {
		width: 12%;
	}

	.name {
		padding: 0;
		border: 0;
		background: none;
		color: var(--text);
		font-family: var(--mono);
		font-size: inherit;
		cursor: text;
		text-align: left;
	}

	.name:hover {
		text-decoration: underline dotted;
		text-underline-offset: 3px;
	}

	.status {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		margin-right: 10px;
		background: var(--text-3);
		opacity: 0.5;
	}

	.status.on {
		background: oklch(0.75 0.18 155);
		opacity: 1;
	}

	.rename {
		width: 160px;
	}
</style>
