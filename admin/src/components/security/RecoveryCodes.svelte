<script lang="ts">
	// The ten recovery codes, shown exactly once (task 77.2). The operator
	// cannot leave this screen without saying they saved them: the server keeps
	// only hashes, so a code not saved now is a code that never existed.
	interface Props {
		codes: string[];
		ondone: () => void;
		doneLabel?: string;
	}

	let { codes, ondone, doneLabel = 'готово' }: Props = $props();
	let saved = $state(false);
	let copied = $state(false);

	function asText(): string {
		const stamp = new Date().toISOString().slice(0, 10);
		return `nekko admin — коды восстановления (${stamp})\nкаждый код работает один раз\n\n${codes.join('\n')}\n`;
	}

	async function copy() {
		try {
			await navigator.clipboard.writeText(asText());
			copied = true;
			setTimeout(() => (copied = false), 2000);
		} catch {
			copied = false;
		}
	}

	function download() {
		const blob = new Blob([asText()], { type: 'text/plain;charset=utf-8' });
		const a = document.createElement('a');
		a.href = URL.createObjectURL(blob);
		a.download = 'nekko-admin-recovery-codes.txt';
		a.click();
		URL.revokeObjectURL(a.href);
	}
</script>

<div class="codes">
	<ol class="grid">
		{#each codes as c (c)}
			<li>{c}</li>
		{/each}
	</ol>

	<div class="row">
		<button class="btn" type="button" onclick={copy}>{copied ? 'скопировано' : 'скопировать'}</button>
		<button class="btn" type="button" onclick={download}>скачать .txt</button>
	</div>

	<label class="check">
		<input type="checkbox" bind:checked={saved} />
		<span>я сохранил коды в надёжном месте</span>
	</label>

	<button class="btn primary" type="button" disabled={!saved} onclick={ondone}>{doneLabel}</button>
</div>

<style>
	.codes {
		display: flex;
		flex-direction: column;
		gap: 14px;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 8px 16px;
		margin: 0;
		padding: 14px 16px 14px 36px;
		font-family: 'IBM Plex Mono', monospace;
		font-size: 14px;
		color: var(--text-primary);
		background: var(--surface-hover);
		border-radius: 8px;
	}

	.grid li {
		white-space: nowrap;
	}

	.row {
		display: flex;
		gap: 8px;
	}

	.check {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 13px;
		color: var(--text-secondary);
		cursor: pointer;
	}

	.btn {
		padding: 9px 14px;
		font-size: 13px;
		font-weight: 500;
		color: var(--text-primary);
		background: var(--surface-hover);
		border: 1px solid var(--border);
		border-radius: 8px;
		cursor: pointer;
	}

	.btn.primary {
		border-color: var(--text-primary);
	}

	.btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	@media (max-width: 420px) {
		.grid {
			grid-template-columns: 1fr;
		}
	}
</style>
