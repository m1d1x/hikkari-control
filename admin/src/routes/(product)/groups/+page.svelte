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
		SERVICE_ICONS,
		emptyRules,
		groupProblem,
		slugify,
		type Group,
		type RuleKind,
	} from '$lib/product/presets';

	// The editor works on a copy: nothing reaches the hub until «сохранить»,
	// and nothing reaches a device until a preset holding the group is released.
	let edit = $state<Group | null>(null);
	let fresh = $state(false);
	let busy = $state(false);
	let error = $state('');
	let query = $state('');
	let nameEl = $state<HTMLInputElement | null>(null);

	const saved = $derived(edit && !fresh ? db.groups.find((x) => x.id === edit!.id) : undefined);
	const dirty = $derived(!!edit && (fresh || JSON.stringify(edit) !== JSON.stringify(saved)));
	const problem = $derived(edit ? groupProblem(edit) : null);

	const missingBundled = $derived(BUNDLED_GROUPS.filter((g) => !db.groups.some((x) => x.id === g.id)));

	const filteredGroups = $derived.by(() => {
		const q = query.trim().toLowerCase();
		if (!q) return db.groups;
		return db.groups.filter((x) => {
			if (x.name.toLowerCase().includes(q)) return true;
			if (x.id.toLowerCase().includes(q)) return true;
			if (x.icon && x.icon.toLowerCase().includes(q)) return true;
			return RULE_KINDS.some((k) => x.rules[k.key]?.some((r) => r.toLowerCase().includes(q)));
		});
	});

	const count = (x: Group) => RULE_KINDS.reduce((n, k) => n + x.rules[k.key].length, 0);
	const usedIn = (id: string) => db.presets.filter((p) => p.bypass.includes(id) || p.proxy.includes(id) || (p.disabled ?? []).includes(id));

	function pick(id: string | undefined) {
		if (dirty && !confirm('несохранённые правки пропадут')) return;
		const g = db.groups.find((x) => x.id === id);
		edit = g ? structuredClone($state.snapshot(g)) : null;
		fresh = false;
		error = '';
	}

	async function populateMissingBundled() {
		const missing = BUNDLED_GROUPS.filter((g) => !db.groups.some((x) => x.id === g.id));
		if (missing.length === 0) return;
		const batchSize = 10;
		for (let i = 0; i < missing.length; i += batchSize) {
			await Promise.all(missing.slice(i, i + batchSize).map((g) => saveGroup(g)));
		}
		db.groups = await fetchGroups();
		if (!edit && db.groups.length > 0) {
			pick(db.groups[0].id);
		}
	}

	onMount(async () => {
		await load();
		// Если база пустая — автоматически заполняем стандартным набором групп клиента
		if (db.groups.length === 0 && !db.error) {
			await act(populateMissingBundled);
		}
		pick(page.url.searchParams.get('g') ?? db.groups[0]?.id);
	});

	async function create() {
		if (dirty && !confirm('несохранённые правки пропадут')) return;
		edit = { id: '', name: '', icon: '', rules: emptyRules() };
		fresh = true;
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
			if (fresh) {
				g.id = slugify(g.name) || 'group';
				let base = g.id;
				let n = 2;
				while (db.groups.some((x) => x.id === g.id)) {
					g.id = `${base}-${n++}`;
				}
			}
			await saveGroup(g);
			db.groups = await fetchGroups();
			fresh = false;
			edit = structuredClone($state.snapshot(db.groups.find((x) => x.id === g.id) ?? g));
		});

	const remove = () =>
		act(async () => {
			if (!edit || !confirm(`удалить группу «${edit.name || edit.id}»?`)) return;
			await deleteGroup(edit.id);
			db.groups = db.groups.filter((x) => x.id !== edit!.id);
			edit = null;
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
			<div class="list-head">
				<button class="btn add" onclick={create}>+ группа</button>
			</div>

			{#if missingBundled.length > 0}
				<div class="sync-banner">
					<button
						class="btn sync-btn"
						onclick={() => act(populateMissingBundled)}
						disabled={busy}
						title="Добавить недостающие стандартные группы (Discord, YouTube, Steam, банки...)"
					>
						⚡ Добавить стандартные группы ({missingBundled.length})
					</button>
				</div>
			{/if}

			{#if db.groups.length > 0}
				<div class="search-box">
					<input
						type="search"
						class="field search-input"
						placeholder="поиск по названию или правилу…"
						bind:value={query}
						onkeydown={(e) => {
							if (e.key === 'Escape') query = '';
						}}
					/>
					{#if query}
						<button class="search-clear" onclick={() => (query = '')} title="очистить" type="button">×</button>
					{/if}
				</div>
			{/if}

			<div class="items-list">
				{#each filteredGroups as x (x.id)}
					<button class="item" class:on={!fresh && x.id === edit?.id} onclick={() => pick(x.id)}>
						<div class="line">
							<span class="name-cell">
								{#if x.icon}<span class="icon-tag mono">{x.icon}</span>{/if}
								<span>{x.name}</span>
							</span>
							<span class="dim n">{count(x)}</span>
						</div>
					</button>
				{:else}
					{#if query.trim()}
						<div class="empty-hint dim">ничего не найдено</div>
					{:else}
						<div class="empty-hint dim">нет групп</div>
					{/if}
				{/each}
			</div>
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
								if (fresh && edit) edit.id = slugify(edit.name);
							}}
						/>
					</label>
					{#if !fresh}<button class="btn" onclick={remove} disabled={busy}>удалить</button>{/if}
					<button class="btn primary" onclick={save} disabled={busy || !dirty || !!problem}>сохранить</button>
				</div>
				<div class="meta-row">
					<label class="icon-box">
						<span class="dim">иконка</span>
						<input
							class="field mono"
							list="service-icons"
							placeholder="например, ru, steam, discord, youtube"
							bind:value={edit.icon}
						/>
						<datalist id="service-icons">
							{#each SERVICE_ICONS as item (item.id)}
								<option value={item.id}>{item.label}</option>
							{/each}
						</datalist>
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
		grid-template-columns: 240px minmax(0, 1fr);
		gap: 16px;
		align-items: start;
	}

	.list {
		padding: 6px;
		display: flex;
		flex-direction: column;
		max-height: calc(100vh - 120px);
		position: sticky;
		top: 16px;
	}

	.list-head {
		flex-shrink: 0;
	}

	.search-box {
		position: relative;
		margin-bottom: 6px;
		flex-shrink: 0;
	}

	.search-input {
		width: 100%;
		padding-right: 24px;
		font-size: 11px;
	}

	.search-clear {
		position: absolute;
		right: 6px;
		top: 50%;
		transform: translateY(-50%);
		background: none;
		border: none;
		color: var(--dim, #888);
		cursor: pointer;
		font-size: 14px;
		line-height: 1;
		padding: 2px 4px;
	}

	.search-clear:hover {
		color: var(--text);
	}

	.items-list {
		overflow-y: auto;
		flex: 1;
		min-height: 0;
	}

	.empty-hint {
		padding: 16px 8px;
		text-align: center;
		font-size: 12px;
	}

	.add {
		width: 100%;
		margin-bottom: 4px;
		text-align: left;
	}

	.sync-banner {
		margin-bottom: 6px;
		flex-shrink: 0;
	}

	.sync-btn {
		width: 100%;
		font-size: 11px;
		padding: 6px 8px;
		background: rgba(80, 160, 255, 0.1);
		border-color: rgba(80, 160, 255, 0.3);
		color: #58a6ff;
		text-align: center;
		white-space: normal;
		line-height: 1.3;
		border-radius: 6px;
	}

	.sync-btn:hover {
		background: rgba(80, 160, 255, 0.18);
	}

	.meta-row {
		margin-top: 8px;
	}

	.icon-box {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
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
		align-items: center;
		justify-content: space-between;
		gap: 8px;
	}

	.name-cell {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}

	.name-cell span:last-child {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.icon-tag {
		display: inline-block;
		font-size: 10px;
		line-height: 1;
		padding: 2px 4px;
		border-radius: 4px;
		background: rgba(255, 255, 255, 0.08);
		color: var(--dim, #888);
		border: 1px solid rgba(255, 255, 255, 0.1);
		flex-shrink: 0;
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
