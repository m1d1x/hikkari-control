<script lang="ts">
	import { onMount, tick } from 'svelte';
	import { page } from '$app/state';
	import { db, load, putPreset } from '$lib/product/store.svelte';
	import {
		createPreset,
		deletePreset,
		refusal,
		releasePreset,
		revertPreset,
		savePreset,
		setStage,
		type Preset,
		type Release,
	} from '$lib/api/presets';
	import {
		SIDES,
		RULE_KINDS,
		groupProblem,
		rulesToWire,
		slugify,
		type Group,
		type Rules,
		type Side,
	} from '$lib/product/presets';

	interface Draft {
		id: string;
		name: string;
		default: boolean;
		bypass: string[];
		proxy: string[];
		disabled: string[];
	}

	// The editor works on a copy of the draft; «сохранить» writes it, and
	// only a release sends anything to devices.
	let edit = $state<Draft | null>(null);
	let fresh = $state(false);
	let busy = $state(false);
	let error = $state('');
	let releaseStage = $state(5);
	/** Where the rollout slider points; follows the live stage until moved. */
	let rolloutStage = $state(0);
	let showJson = $state(false);
	let nameEl = $state<HTMLInputElement | null>(null);

	const p = $derived(edit && !fresh ? db.presets.find((x) => x.id === edit!.id) : undefined);
	const draftOf = (x: Preset): Draft => ({
		id: x.id,
		name: x.name,
		default: x.default,
		bypass: [...x.bypass],
		proxy: [...x.proxy],
		disabled: [...(x.disabled ?? [])],
	});
	const dirty = $derived(!!edit && (fresh || !p || JSON.stringify(edit) !== JSON.stringify(draftOf(p))));

	function pick(id: string | undefined) {
		if (dirty && !confirm('несохранённые правки пропадут')) return;
		const x = db.presets.find((y) => y.id === id);
		edit = x ? draftOf(x) : null;
		fresh = false;
		error = '';
		showJson = false;
	}

	onMount(async () => {
		await load();
		pick(page.url.searchParams.get('p') ?? db.presets[0]?.id);
	});

	const latest = (x: Preset) => x.next ?? x.current;
	const groupOf = (id: string) => db.groups.find((g) => g.id === id);
	const ruleCount = (r: Rules) => RULE_KINDS.reduce((n, k) => n + r[k.key].length, 0);

	function plural(n: number, [one, few, many]: [string, string, string]): string {
		const d = n % 10;
		const h = n % 100;
		return `${n} ${d === 1 && h !== 11 ? one : d >= 2 && d <= 4 && (h < 12 || h > 14) ? few : many}`;
	}

	/** Rules added and dropped between two states of one group, OS named for the per-OS lists. */
	function ruleDiff(was: Rules, now: Rules): string[] {
		return RULE_KINDS.flatMap(({ key, os }) => {
			const tag = os ? ` (${os})` : '';
			return [
				...now[key].filter((v) => !was[key].includes(v)).map((v) => `+ ${v}${tag}`),
				...was[key].filter((v) => !now[key].includes(v)).map((v) => `− ${v}${tag}`),
			];
		});
	}

	/** What a release would change against the latest one: groups in and out, and edits inside groups. */
	function changes(x: Preset): { text: string; kind: 'add' | 'drop' | 'edit' }[] {
		const base = latest(x);
		return SIDES.flatMap(({ key, label }) => {
			const was = base?.[key] ?? [];
			const out: { text: string; kind: 'add' | 'drop' | 'edit' }[] = [];
			for (const id of x[key] ?? []) {
				const g = groupOf(id);
				const old = was.find((o) => o.id === id);
				if (!g) continue;
				if (!old) out.push({ text: `+ ${g.name} → ${label}`, kind: 'add' });
				else {
					const d = ruleDiff(old.rules, g.rules);
					if (d.length || old.name !== g.name)
						out.push({ text: `${g.name}: ${d.join('  ') || 'название'}`, kind: 'edit' });
				}
			}
			for (const old of was) {
				if (!x[key]?.includes(old.id)) out.push({ text: `− ${old.name} из «${label}»`, kind: 'drop' });
			}
			return out;
		});
	}

	const diff = $derived(p ? changes(p) : []);
	const free = $derived(
		db.groups.filter(
			(g) =>
				edit &&
				!edit.bypass.includes(g.id) &&
				!edit.proxy.includes(g.id) &&
				!(edit.disabled ?? []).includes(g.id),
		),
	);
	/** Quick checks while editing; the hub runs the client's full ones on release. */
	const problems = $derived(
		p
			? [...p.bypass, ...p.proxy, ...(p.disabled ?? [])]
					.map(groupOf)
					.filter((g): g is Group => !!g)
					.flatMap((g) => {
						const why = groupProblem(g);
						return why ? [`${g.name || g.id}: ${why}`] : [];
					})
			: [],
	);

	/** The services of a release exactly as a client gets them; the version it is served under is the hub's. */
	function servicesOf(r: Release) {
		return SIDES.flatMap(({ key }) =>
			(r[key] ?? []).map((g) => ({
				id: g.id,
				name: g.name,
				icon: g.icon,
				...rulesToWire(g.rules),
				defaultModes: key === 'disabled' ? [] : [key],
			})),
		);
	}
	const json = $derived(p && latest(p) ? JSON.stringify(servicesOf(latest(p)!), null, 2) : '');

	function status(x: Preset): { text: string; cls: string } {
		if (x.next) return { text: `${x.stage}%`, cls: 'on' };
		if (!x.current) return { text: 'черновик', cls: 'dim' };
		if (changes(x).length) return { text: 'изменён', cls: 'dim' };
		return { text: `v${x.current.v}`, cls: 'dim' };
	}

	/** A preset no device is assigned to: not the default and claiming no country. */
	const unserved = (x: Preset) => !x.default && x.countries.length === 0;

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

	async function create() {
		if (dirty && !confirm('несохранённые правки пропадут')) return;
		edit = { id: '', name: '', default: db.presets.length === 0, bypass: [], proxy: [], disabled: [] };
		fresh = true;
		error = '';
		await tick();
		nameEl?.focus();
	}

	const save = () =>
		act(async () => {
			const d = $state.snapshot(edit!) as Draft;
			if (fresh) {
				d.id = slugify(d.name) || 'preset';
				let base = d.id;
				let n = 2;
				while (db.presets.some((x) => x.id === d.id)) {
					d.id = `${base}-${n++}`;
				}
			}
			const body = {
				name: d.name,
				default: d.default,
				countries: p?.countries ?? [],
				bypass: d.bypass,
				proxy: d.proxy,
				disabled: d.disabled ?? [],
			};
			const saved = fresh ? await createPreset(d.id, body) : await savePreset(d.id, body);
			// Taking the default flag clears it elsewhere: reread them all.
			if (d.default) await load();
			else putPreset(saved);
			fresh = false;
			edit = draftOf(saved);
		});

	const remove = () =>
		act(async () => {
			if (!edit || !confirm(`удалить пресет «${edit.name || edit.id}»? его устройства перейдут на пресет по умолчанию`))
				return;
			await deletePreset(edit.id);
			db.presets = db.presets.filter((x) => x.id !== edit!.id);
			edit = null;
			pick(db.presets[0]?.id);
		});

	$effect(() => {
		rolloutStage = p?.next ? p.stage : 0;
	});

	const release = () => act(async () => putPreset(await releasePreset(p!.id, releaseStage)));
	const stage = (to: number) => act(async () => putPreset(await setStage(p!.id, to)));
	const revert = () =>
		act(async () => {
			if (
				!confirm(
					`откатить v${p!.next!.v}? все устройства вернутся на ${p!.current ? `v${p!.current.v}` : 'встроенный каталог клиента'}`,
				)
			)
				return;
			putPreset(await revertPreset(p!.id));
		});

	function addGroup(side: Side, id: string) {
		if (id && edit) edit[side] = [...edit[side], id];
	}

	function dropGroup(side: Side, id: string) {
		if (edit) edit[side] = edit[side].filter((x) => x !== id);
	}
</script>

<div class="head">
	<div>
		<h1>пресеты</h1>
		<div class="dim sub">набор групп, который клиент забирает с сервера; раскатывается целиком</div>
	</div>
</div>

{#if db.error}
	<div class="card bad">хаб не ответил: {db.error}</div>
{:else if db.loaded}
	<div class="presets">
		<section class="card list">
			<button class="btn add" onclick={create}>+ пресет</button>
			{#each db.presets as x (x.id)}
				{@const st = status(x)}
				<button class="item" class:on={!fresh && x.id === edit?.id} onclick={() => pick(x.id)}>
					<div class="line">
						<span>{x.name}</span>
						<span class="st {st.cls}">{st.text}</span>
					</div>
					<div class="dim meta">
						{plural(x.bypass.length + x.proxy.length + (x.disabled?.length ?? 0), ['группа', 'группы', 'групп'])}{x.default
							? ' · по умолчанию'
							: ''}
					</div>
				</button>
			{/each}
		</section>

		{#if edit}
			<section class="card">
				<div class="top">
					<label class="name-box">
						<span class="dim">название</span>
						<input
							class="field name-input"
							placeholder="например, Россия или Общий"
							bind:this={nameEl}
							bind:value={edit.name}
							oninput={() => {
								if (fresh && edit) edit.id = slugify(edit.name);
							}}
						/>
					</label>
					{#if !fresh}<button class="btn" onclick={remove} disabled={busy || edit.default}>удалить</button>{/if}
					<button class="btn primary" onclick={save} disabled={busy || !dirty || !edit.name.trim()}>сохранить</button>
				</div>
				<div class="preset-meta">
					{#if p?.default}
						<div class="default-badge">★ пресет по умолчанию — его получают все устройства</div>
					{:else}
						<label class="check">
							<input type="checkbox" bind:checked={edit.default} />
							<span>по умолчанию — его получают все устройства</span>
						</label>
					{/if}
				</div>
				{#if p && unserved(p)}
					<div class="dim hint">не раздаётся: устройства получают только пресет по умолчанию</div>
				{/if}
				{#if error}
					<div class="bad problem">{error}</div>
				{/if}

				<div class="sides">
					{#each SIDES as s (s.key)}
						<div>
							<div class="label">{s.label}</div>
							<div class="groups">
								{#each edit[s.key] as id (id)}
									{@const g = groupOf(id)}
									<span class="g" class:off={s.key === 'disabled'}>
										<a href="/groups?g={id}">{g?.name ?? id}</a>
										{#if g}<small>{ruleCount(g.rules)}</small>{/if}
										<button onclick={() => dropGroup(s.key, id)} title="убрать">×</button>
									</span>
								{/each}
								{#if free.length}
									<select
										class="pick"
										onchange={(e) => {
											addGroup(s.key, e.currentTarget.value);
											e.currentTarget.value = '';
										}}
									>
										<option value="">+ группа</option>
										{#each free as g (g.id)}
											<option value={g.id}>{g.name}</option>
										{/each}
									</select>
								{/if}
							</div>
						</div>
					{/each}
				</div>

				{#if p && p.bypass.length + p.proxy.length + (p.disabled?.length ?? 0) > 0}
					<div class="block">
						<div class="release">
							<div class="label">что уйдёт людям в v{(latest(p)?.v ?? 0) + 1}</div>
							{#if dirty}
								<span class="dim hint">сначала сохрани пресет</span>
							{:else if problems.length}
								<span class="bad hint">клиент не примет каталог: {problems.join('; ')}</span>
							{:else}
								<div class="actions">
									{#if p.next}<span class="dim hint">заменит раскатку v{p.next.v}</span>{/if}
									<label class="share">
										<input type="range" min="1" max="100" bind:value={releaseStage} />
										<span class="mono">{releaseStage === 100 ? 'всем' : `${releaseStage}%`}</span>
									</label>
									<button class="btn primary" onclick={release} disabled={busy}
										>выпустить v{(latest(p)?.v ?? 0) + 1}</button
									>
								</div>
							{/if}
						</div>
						<div class="diff">
							{#if !diff.length}
								<div class="dim">без изменений — повтор v{latest(p)?.v} под новой версией</div>
							{/if}
							{#each diff as d (d.text)}
								<div class:ok={d.kind === 'add'} class:bad={d.kind === 'drop'}>{d.text}</div>
							{/each}
						</div>
					</div>
				{/if}

				{#if p?.next}
					<div class="block">
						<div class="release">
							<span class="label"
								>раскатка v{p.next.v} · {p.stage}% устройств, остальные на {p.current
									? `v${p.current.v}`
									: 'встроенном каталоге'}</span
							>
							<div class="actions">
								<button class="btn" onclick={revert} disabled={busy}>откатить</button>
								<button class="btn primary" onclick={() => stage(100)} disabled={busy}>всем</button>
							</div>
						</div>
						<div class="actions rollout">
							<label class="share">
								<input type="range" min="1" max="100" bind:value={rolloutStage} />
								<span class="mono">{rolloutStage}%</span>
							</label>
							<button
								class="btn"
								onclick={() => stage(rolloutStage)}
								disabled={busy || rolloutStage === p.stage}>применить</button
							>
						</div>
					</div>
				{:else if p?.current}
					<div class="block dim">v{p.current.v} у всех · выпустил {p.current.createdBy || '—'}</div>
				{/if}

				{#if json}
					<div class="block">
						<div class="release">
							<span class="label">группы v{latest(p!)?.v} для клиента</span>
							<div class="actions">
								<button class="btn" onclick={() => navigator.clipboard.writeText(json)}>скопировать</button>
								<button class="btn" onclick={() => (showJson = !showJson)}>{showJson ? 'скрыть' : 'показать'}</button>
							</div>
						</div>
						{#if showJson}
							<pre class="json mono">{json}</pre>
						{/if}
					</div>
				{/if}
			</section>
		{/if}
	</div>
{/if}

<style>
	.preset-meta {
		margin: 8px 0 14px;
		font-size: 12px;
	}

	.default-badge {
		color: var(--primary, #8ab4f8);
		font-size: 12px;
	}

	.problem {
		margin-top: 6px;
		font-size: 12px;
	}

	.sub {
		font-size: 12px;
	}

	.presets {
		display: grid;
		grid-template-columns: 220px minmax(0, 1fr);
		gap: 16px;
		align-items: start;
	}

	/* ---------- preset list ---------- */

	.list {
		padding: 6px;
	}

	.add {
		width: 100%;
		margin-bottom: 4px;
		text-align: left;
	}

	.item {
		display: block;
		width: 100%;
		text-align: left;
		padding: 8px 10px;
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

	.st,
	.meta {
		font-size: 11px;
	}

	.st.on {
		color: var(--primary);
	}

	/* ---------- editor ---------- */

	.top {
		display: flex;
		align-items: center;
		gap: 16px;
		margin-bottom: 18px;
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

	.label {
		font-size: 11px;
		color: var(--text-3);
	}

	.sides {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 20px;
	}

	.sides .label {
		margin-bottom: 8px;
	}

	.groups {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.g {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 3px 4px 3px 10px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--inset);
		font-size: 12px;
	}

	.g.off {
		opacity: 0.7;
		border-style: dashed;
	}

	.g a:hover {
		color: var(--primary);
	}

	.g small {
		font-family: var(--mono);
		font-size: 10px;
		color: var(--text-3);
	}

	.g button {
		padding: 0 4px;
		color: var(--text-3);
	}

	.g button:hover {
		color: var(--bad);
	}

	.pick {
		padding: 3px 8px;
		border: 1px dashed var(--border);
		border-radius: 6px;
		background: none;
		color: var(--text-2);
		font: inherit;
		font-size: 12px;
		outline: none;
		cursor: pointer;
	}

	.pick option {
		background: var(--surface);
		color: var(--text);
	}

	.block {
		margin-top: 18px;
		padding-top: 14px;
		border-top: 1px solid var(--border-subtle);
	}

	.release {
		display: flex;
		align-items: center;
		gap: 16px;
		margin-bottom: 10px;
	}

	.release .btn,
	.release .hint {
		margin-left: auto;
	}

	.hint {
		font-size: 11px;
	}

	.diff {
		display: flex;
		flex-direction: column;
		gap: 3px;
		font-family: var(--mono);
		font-size: 12px;
		color: var(--text-2);
		white-space: pre-wrap;
	}

	.actions {
		margin-left: auto;
		display: flex;
		gap: 6px;
	}

	.share {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.share input {
		width: 200px;
		accent-color: var(--primary);
	}

	.share span {
		min-width: 36px;
		font-size: 12px;
	}

	.rollout {
		margin-left: 0;
		align-items: center;
	}

	.json {
		max-height: 320px;
		overflow: auto;
		margin: 8px 0 0;
		padding: 10px;
		border-radius: 6px;
		background: var(--surface);
		font-size: 11px;
		color: var(--text-2);
	}
</style>
