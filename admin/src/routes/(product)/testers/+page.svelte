<script lang="ts">
	import { onMount } from 'svelte';
	import { ago, num } from '$lib/product/format';
	import {
		exportTesterLog,
		fetchTesterChunks,
		fetchTesterInstalls,
		type TesterChunk,
		type TesterInstall,
	} from '$lib/api/support';

	// Live logs of installs in tester mode. The client ships its sanitized
	// engine log a chunk a minute; the hub keeps 7 days. "Следить" polls for
	// chunks newer than the last one shown.
	const POLL_MS = 15_000;

	let installs = $state<TesterInstall[]>([]);
	let current = $state<string | null>(null);
	let chunks = $state<TesterChunk[]>([]);
	let query = $state('');
	let follow = $state(true);
	let error = $state('');
	let loading = $state(false);
	let copied = $state(false);
	let pre = $state<HTMLPreElement | null>(null);

	// Export for an LLM: the hub builds NDJSON for the chosen window (meta
	// with a guide to the log sources, summary, one record per line).
	const PERIODS = [
		{ label: '15 мин', secs: 15 * 60 },
		{ label: '1 ч', secs: 3600 },
		{ label: '6 ч', secs: 6 * 3600 },
		{ label: '24 ч', secs: 24 * 3600 },
		{ label: '7 дн', secs: 7 * 86400 },
	];
	let period = $state(3600);
	let exporting = $state<'copy' | 'file' | null>(null);
	let exported = $state('');

	async function exportForLLM(mode: 'copy' | 'file') {
		if (!current) return;
		exporting = mode;
		exported = '';
		error = '';
		try {
			const to = Math.floor(Date.now() / 1000);
			const ndjson = await exportTesterLog(current, to - period, to);
			const kb = Math.round(ndjson.length / 1024);
			if (mode === 'copy') {
				await navigator.clipboard.writeText(ndjson);
				exported = `скопировано ${kb} КБ NDJSON`;
			} else {
				const stamp = new Date().toISOString().slice(0, 16).replace(/[:T]/g, '-');
				const url = URL.createObjectURL(new Blob([ndjson], { type: 'application/x-ndjson;charset=utf-8' }));
				const a = document.createElement('a');
				a.href = url;
				a.download = `tester-${current}-${stamp}.ndjson`;
				a.click();
				setTimeout(() => URL.revokeObjectURL(url), 1000);
				exported = `скачано ${kb} КБ`;
			}
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			exporting = null;
		}
	}

	const selected = $derived(installs.find((i) => i.install_id === current) ?? null);
	const lines = $derived(
		chunks.flatMap((c) => [
			...(c.dropped > 0 ? [`--- клиент выбросил ${c.dropped} строк: hub был недоступен ---`] : []),
			...c.body.split('\n'),
		]),
	);
	const shown = $derived.by(() => {
		const q = query.trim().toLowerCase();
		return q ? lines.filter((l) => l.toLowerCase().includes(q)) : lines;
	});

	async function loadInstalls() {
		try {
			installs = (await fetchTesterInstalls()).installs;
			if (!current && installs.length > 0) await select(installs[0].install_id);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function select(id: string) {
		current = id;
		chunks = [];
		loading = true;
		error = '';
		try {
			chunks = (await fetchTesterChunks(id)).chunks;
			scrollDown();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			loading = false;
		}
	}

	async function poll() {
		if (!follow || !current || loading) return;
		const last = chunks.at(-1);
		const since = last ? Math.floor(new Date(last.received_at).getTime() / 1000) : undefined;
		try {
			const fresh = (await fetchTesterChunks(current, since)).chunks.filter((c) => !chunks.some((o) => o.id === c.id));
			if (fresh.length > 0) {
				chunks = [...chunks, ...fresh];
				scrollDown();
			}
			installs = (await fetchTesterInstalls()).installs;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	function scrollDown() {
		queueMicrotask(() => pre && (pre.scrollTop = pre.scrollHeight));
	}

	async function copy() {
		await navigator.clipboard.writeText(shown.join('\n'));
		copied = true;
		setTimeout(() => (copied = false), 1500);
	}

	function download() {
		const url = URL.createObjectURL(new Blob([lines.join('\n')], { type: 'text/plain' }));
		const a = document.createElement('a');
		a.href = url;
		a.download = `tester-${current}.log`;
		a.click();
		URL.revokeObjectURL(url);
	}

	onMount(() => {
		void loadInstalls();
		const t = setInterval(poll, POLL_MS);
		return () => clearInterval(t);
	});
</script>

<div class="head">
	<h1>логи тестеров</h1>
	<span class="dim">включается на клиенте: 7 быстрых тапов по заголовку · хранится 7 дней</span>
</div>

{#if error}<p class="warn">не загрузилось: {error}</p>{/if}

<div class="layout">
	<section class="card list">
		{#if installs.length === 0}
			<p class="dim">ни одна установка в режиме тестера пока ничего не прислала</p>
		{/if}
		{#each installs as i (i.install_id)}
			<button class="inst" class:on={i.install_id === current} onclick={() => select(i.install_id)}>
				<div class="id">{i.install_id}</div>
				<div class="dim">
					{i.platform} · {i.version} · {num(i.lines)} строк · {ago(new Date(i.last_seen).getTime())} назад
					{#if i.dropped > 0}· <span class="warn">потеряно {num(i.dropped)}</span>{/if}
				</div>
			</button>
		{/each}
	</section>

	<section class="card log">
		{#if selected}
			<div class="top">
				<input class="field" placeholder="фильтр: diag:stun, proxy-delay, error, discord…" bind:value={query} />
				<label class="dim"><input type="checkbox" bind:checked={follow} /> следить</label>
				<button class="chip" onclick={copy}>{copied ? 'скопировано' : 'копировать'}</button>
				<button class="chip" onclick={download}>скачать</button>
			</div>
			<div class="llm">
				<span class="dim">для LLM за</span>
				{#each PERIODS as p (p.secs)}
					<button class="chip" class:on={period === p.secs} onclick={() => (period = p.secs)}>{p.label}</button>
				{/each}
				<button class="chip" disabled={!!exporting} onclick={() => exportForLLM('copy')}>
					{exporting === 'copy' ? 'собираю…' : 'копировать NDJSON'}
				</button>
				<button class="chip" disabled={!!exporting} onclick={() => exportForLLM('file')}>
					{exporting === 'file' ? 'собираю…' : 'скачать .ndjson'}
				</button>
				{#if exported}<span class="dim">{exported}</span>{/if}
			</div>
			<div class="dim meta">
				{num(shown.length)} из {num(lines.length)} строк · с {new Date(selected.first_seen).toLocaleString('ru')}
				{#if loading}· загружаю…{/if}
			</div>
			<pre bind:this={pre}>{shown.join('\n')}</pre>
		{:else}
			<p class="dim">выберите установку</p>
		{/if}
	</section>
</div>

<style>
	.layout {
		display: grid;
		grid-template-columns: 300px 1fr;
		gap: 16px;
		align-items: start;
	}
	.list {
		display: flex;
		flex-direction: column;
		gap: 4px;
		max-height: 80vh;
		overflow: auto;
	}
	.inst {
		text-align: left;
		padding: 8px 10px;
		border-radius: 6px;
		background: none;
		border: 1px solid transparent;
		color: inherit;
		cursor: pointer;
	}
	.inst.on {
		border-color: var(--border-subtle);
		background: var(--bg);
	}
	.id {
		font-family: var(--mono, monospace);
		font-size: 12px;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.top {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.top .field {
		flex: 1;
	}
	.llm {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		align-items: center;
		margin-top: 10px;
	}
	.meta {
		margin: 8px 0;
		font-size: 12px;
	}
	pre {
		max-height: 70vh;
		overflow: auto;
		font-size: 11px;
		line-height: 1.45;
		white-space: pre-wrap;
		word-break: break-all;
	}
	@media (max-width: 900px) {
		.layout {
			grid-template-columns: 1fr;
		}
	}
</style>
