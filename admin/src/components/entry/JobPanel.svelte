<script lang="ts">
	import { onDestroy, untrack } from 'svelte';
	import { describeEntryError, fetchJob, jobShape, type Job, type JobLine } from '$lib/api/entry';
	import { formatRelativeTime } from '$lib/utils/format';

	interface Props {
		jobId: string;
		/** The job ended (done or failed): the page reloads the pool. */
		onfinished?: (job: Job) => void;
	}

	let { jobId, onfinished }: Props = $props();

	let job = $state<Job | null>(null);
	let lines = $state<JobLine[]>([]);
	let error = $state<string | null>(null);
	let logEl = $state<HTMLElement | null>(null);
	let timer: ReturnType<typeof setTimeout> | null = null;
	let following = '';

	// Poll while the job runs: new lines only (after the last seq seen).
	async function tick(id: string) {
		try {
			const after = lines.length ? lines[lines.length - 1].seq : 0;
			const res = await fetchJob(id, after);
			if (id !== following) return;
			job = res.job;
			if (res.lines.length) {
				lines = [...lines, ...res.lines];
				queueMicrotask(() => logEl?.scrollTo({ top: logEl.scrollHeight }));
			}
			error = null;
			if (res.job.status !== 'running') {
				onfinished?.(res.job);
				return;
			}
		} catch (e) {
			error = describeEntryError(e);
		}
		timer = setTimeout(() => tick(id), 1000);
	}

	$effect(() => {
		const id = jobId;
		if (timer) clearTimeout(timer);
		following = id;
		// untrack: tick reads `lines`; tracked, every new line would re-run this
		// effect, which empties the log — the panel stood blank while the job ran.
		untrack(() => {
			job = null;
			lines = [];
			tick(id);
		});
	});

	onDestroy(() => {
		if (timer) clearTimeout(timer);
		following = '';
	});

	const shape = $derived(jobShape(job?.kind));
	const current = $derived(job ? shape.steps.findIndex((s) => job!.step.split(' ')[0] === s.id) : -1);

	function stepState(i: number): 'done' | 'now' | 'failed' | 'todo' {
		if (!job) return 'todo';
		if (job.status === 'done') return 'done';
		if (i < current) return 'done';
		if (i === current) return job.status === 'failed' ? 'failed' : 'now';
		return 'todo';
	}

	const STATUS_TEXT = { running: 'идёт', done: 'готово', failed: 'упало' } as const;
</script>

<section class="card">
	<div class="card-head">
		<h2>
			{shape.title}
			{job?.box ?? ''}
			{#if job}<span class="status {job.status}">{STATUS_TEXT[job.status]}</span>{/if}
		</h2>
		{#if job}<span class="meta">начат {formatRelativeTime(job.createdAt)}</span>{/if}
	</div>
	<ol class="steps">
		{#each shape.steps as s, i (s.id)}
			<li class={stepState(i)}>
				<span class="dot"></span>
				{s.label}
				{#if s.id === 'deploy' && stepState(i) === 'now' && job}
					<span class="meta mono">{job.step.split(' ')[1] ?? ''}</span>
				{/if}
			</li>
		{/each}
	</ol>
	{#if job?.status === 'failed' && job.error}<p class="err" role="alert">{job.error}</p>{/if}
	<div class="log" bind:this={logEl}>
		{#each lines as l (l.seq)}
			<div><span class="at">{new Date(l.at).toLocaleTimeString('ru-RU')}</span>{l.line}</div>
		{:else}
			<div class="meta">жду первую строку</div>
		{/each}
	</div>
	{#if error}<p class="err" role="alert">{error}</p>{/if}
</section>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin: 4px 0 8px;
		padding: 12px 14px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
	}

	.card-head {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: 12px;
		flex-wrap: wrap;
	}

	h2 {
		display: flex;
		align-items: baseline;
		gap: 10px;
		margin: 0;
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.status {
		font-size: 12px;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.status.done {
		color: var(--status-success, #3fb950);
	}

	.status.failed {
		color: #e25555;
	}

	.steps {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 14px;
		margin: 0;
		padding: 0;
		list-style: none;
		font-size: 12px;
		color: var(--text-secondary);
	}

	.steps li {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--border);
	}

	.steps li.done {
		color: var(--text-primary);
	}

	.steps li.done .dot {
		background: var(--status-success, #3fb950);
	}

	.steps li.now {
		color: var(--text-primary);
		font-weight: 600;
	}

	.steps li.now .dot {
		background: var(--primary);
		animation: pulse 1s ease-in-out infinite alternate;
	}

	.steps li.failed {
		color: #e25555;
	}

	.steps li.failed .dot {
		background: #e25555;
	}

	@keyframes pulse {
		from {
			opacity: 0.4;
		}
		to {
			opacity: 1;
		}
	}

	.log {
		max-height: 220px;
		overflow-y: auto;
		padding: 10px 12px;
		background: var(--surface-hover);
		border-radius: 8px;
		font-family: 'IBM Plex Mono', monospace;
		font-size: 12px;
		line-height: 1.6;
		color: var(--text-primary);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}

	.at {
		margin-right: 10px;
		color: var(--text-secondary);
	}

	.mono {
		font-family: 'IBM Plex Mono', monospace;
	}

	.meta {
		font-size: 12px;
		color: var(--text-secondary);
	}

	.err {
		margin: 0;
		color: #e25555;
		font-size: 13px;
		overflow-wrap: anywhere;
	}
</style>
