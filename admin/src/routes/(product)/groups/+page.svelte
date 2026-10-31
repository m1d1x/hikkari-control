<script lang="ts">
	import { onMount, tick } from 'svelte';
	import { page } from '$app/state';
	import RuleList from './RuleList.svelte';
	import { db, load } from '$lib/product/store.svelte';
	import { deleteGroup, fetchGroups, normalizeRule, refusal, saveGroup } from '$lib/api/presets';
	import {
		BUNDLED_GROUPS,
		RULE_KINDS,
		RULE_SECTIONS,
		emptyRules,
		groupProblem,
		slugify,
		type Group,
		type RuleKind,
	} from '$lib/product/presets';

	// The editor works on a copy: nothing reaches the hub until «сохранить»,
	// and nothing reaches a device until a preset holding the group is released.
	let edit = $state<Group | null>(null);
	/** the id is the hub's key, so it is typed once, before the first save */
	let fresh = $state(false);
	let idEdited = $state(false);
	let busy = $state(false);
	let error = $state('');
	let nameEl = $state<HTMLInputElement | null>(null);

	const saved = $derived(edit && !fresh ? db.groups.find((x) => x.id === edit!.id) : undefined);
	const dirty = $derived(!!edit && (fresh || JSON.stringify(edit) !== JSON.stringify(saved)));
	const problem = $derived(edit ? groupProblem(edit) : null);

	const count = (x: Group) => RULE_KINDS.reduce((n, k) => n + x.rules[k.key].length, 0);
	const usedIn = (id: string) => db.presets.filter((p) => p.bypass.includes(id) || p.proxy.includes(id) || (p.disabled ?? []).includes(id));

	function pick(id: string | undefined) {
		if (dirty && !confirm('несохранённые правки пропадут')) return;
		const g = db.groups.find((x) => x.id === id);
		edit = g ? structuredClone($state.snapshot(g)) : null;
		fresh = false;
		idEdited = false;
		error = '';
	}

	onMount(async () => {
		await load();
		pick(page.url.searchParams.get('g') ?? db.groups[0]?.id);
	});

	async function create() {
		if (dirty && !confirm('несохранённые правки пропадут')) return;
		edit = { id: '', name: '', icon: '', rules: emptyRules() };
		fresh = true;
		idEdited = false;
		error = '';
		await tick();
		nameEl?.focus();
	}

	async function act(fn: () => Promise<void>) {
		busy = true;
		error = '';
		try {
			await fn();
		} catch (e) {
			error = refusal(e);
		} finally {
			busy = false;
		}
	}

	const save = () =>
		act(async () => {
			const g = $state.snapshot(edit!) as Group;
			await saveGroup(g);
			db.groups = await fetchGroups();
			fresh = false;
			edit = structuredClone(db.groups.find((x) => x.id === g.id) ?? g);
		});

	const remove = () =>
		act(async () => {
			if (!edit || !confirm(`удалить группу «${edit.name || edit.id}»?`)) return;
			await deleteGroup(edit.id);
			db.groups = db.groups.filter((x) => x.id !== edit!.id);
			edit = null;
			pick(db.groups[0]?.id);
		});

	/** An empty hub starts from the groups the client ships with. */
	const seed = () =>
		act(async () => {
			for (const g of BUNDLED_GROUPS) await saveGroup(g);
			db.groups = await fetchGroups();
			pick(db.groups[0]?.id);
		});

	const normalizer = (k: RuleKind) => (v: string) =>
		normalizeRule(k, v).catch((e) => {
			throw new Error(refusal(e));
		});
</script>

<div class="head">
	<div>
		<h1>группы</h1>
		<div class="dim sub">всё, что касается одной штуки: домены, ip и приложения стима, дискорда, банков</div>
	</div>
</div>

{#if db.error}
	<div class="card bad">хаб не ответил: {db.error}</div>
{:else if db.loaded}
	<div class="groups">
		<section class="card list">
			<button class="btn add" onclick={create}>+ группа</button>
			{#each db.groups as x (x.id)}
				<button class="item" class:on={!fresh && x.id === edit?.id} onclick={() => pick(x.id)}>
					<div class="line">
						<span>{x.name}</span>
						<span class="dim n">{count(x)}</span>
					</div>
				</button>
			{:else}
				<button class="btn seed" onclick={seed} disabled={busy}>взять встроенные группы клиента</button>
			{/each}
		</section>

		{#if edit}
			<section class="card">
				<div class="top">
					<label class="name-box">
						<span class="dim">название</span>
						<input
							class="field name-input"
							placeholder="например, Steam или Discord"
							bind:this={nameEl}
							bind:value={edit.name}
							oninput={() => {
								if (fresh && !idEdited && edit) edit.id = slugify(edit.name);
							}}
						/>
					</label>
					{#if !fresh}<button class="btn" onclick={remove} disabled={busy}>удалить</button>{/if}
					<button class="btn primary" onclick={save} disabled={busy || !dirty || !!problem}>сохранить</button>
				</div>
				<div class="ids">
					<label>
						<span class="dim">id</span>
						<input
							class="field mono"
							placeholder="steam"
							bind:value={edit.id}
							oninput={() => {
								idEdited = true;
							}}
							disabled={!fresh}
						/>
					</label>
					<label>
						<span class="dim">иконка</span>
						<input class="field mono" placeholder="id иконки в клиенте" bind:value={edit.icon} />
					</label>
				</div>
				{#if problem}
					<div class="bad problem">клиент не примет: {problem}</div>
				{/if}
				{#if error}
					<div class="bad problem">{error}</div>
				{/if}
				<div class="dim used">
					{#if usedIn(edit.id).length}
						в пресетах:
						{#each usedIn(edit.id) as p, i (p.id)}{i ? ', ' : ' '}<a href="/presets?p={p.id}">{p.name}</a>{/each}
						· правки уйдут людям со следующим выпуском пресета
					{:else}
						ни в одном пресете
					{/if}
				</div>

				{#each RULE_SECTIONS as s (s.label)}
					<div class="section">
						<div class="os">{s.label}</div>
						{#each s.kinds as k (k.key)}
							<RuleList label={k.label} hint={k.hint} bind:items={edit.rules[k.key]} normalize={normalizer(k.key)} />
						{/each}
					</div>
				{/each}
				<p class="dim note">
					это сервис каталога движка (<span class="mono">engine/core/presets</span>): каждый клиент берёт «везде» и свою
					ос. папки — кусок пути без диска и имени юзера, ловит все бинари под ним, где бы ни стояло: игры лаунчеров (<span
						class="mono">steamapps/common/</span
					>), хелперы внутри .app (<span class="mono">Discord.app/</span>)
				</p>
			</section>
		{/if}
	</div>
{/if}

<style>
	.sub {
		font-size: 12px;
	}

	.groups {
		display: grid;
		grid-template-columns: 220px minmax(0, 1fr);
		gap: 16px;
		align-items: start;
	}

	.list {
		padding: 6px;
	}

	.add,
	.seed {
		width: 100%;
		margin-bottom: 4px;
		text-align: left;
	}

	.ids {
		display: flex;
		gap: 16px;
		margin-top: 8px;
		font-size: 12px;
	}

	.ids label {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.item {
		display: block;
		width: 100%;
		text-align: left;
		padding: 7px 10px;
		border-radius: 6px;
	}

	.item:hover,
	.item.on {
		background: var(--surface);
	}

	.line {
		display: flex;
		justify-content: space-between;
		gap: 8px;
	}

	.n {
		font-family: var(--mono);
		font-size: 11px;
	}

	.top {
		display: flex;
		align-items: center;
		gap: 16px;
	}

	.name-box {
		flex: 1;
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
	}

	.name-input {
		font-size: 14px;
		font-weight: 600;
	}

	.used {
		margin-top: 2px;
		font-size: 12px;
	}

	.used a {
		color: var(--text-2);
	}

	.used a:hover {
		color: var(--text);
	}

	.problem {
		margin-top: 6px;
		font-size: 12px;
	}

	.note {
		margin-top: 16px;
		font-size: 11px;
	}

	/* one row per OS: its name, then its rule lists */
	.section {
		display: grid;
		grid-template-columns: 90px repeat(2, minmax(0, 1fr));
		gap: 24px;
		margin-top: 16px;
		padding-top: 14px;
		border-top: 1px solid var(--border-subtle);
	}

	.os {
		font-size: 12px;
		color: var(--text-2);
	}
</style>
