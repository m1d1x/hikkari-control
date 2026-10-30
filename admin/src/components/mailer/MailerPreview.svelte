<script lang="ts">
	// Preview panel — shows the rendered HTML in an iframe (srcdoc, no
	// network call), with a toggle to flip to the plaintext view. The
	// subject is shown as a faux-inbox header above the body so the
	// operator gets a visual cue close to what the recipient actually sees.
	interface Props {
		subject: string;
		htmlBody: string;
		textBody: string;
		loading?: boolean;
		error?: string | null;
	}

	let { subject, htmlBody, textBody, loading = false, error = null }: Props = $props();

	let view: 'html' | 'plain' = $state('html');
</script>

<div class="preview" class:dim={loading}>
	<div class="preview-header">
		<div class="title">Превью</div>
		<div class="view-toggle" role="tablist">
			<button
				type="button"
				role="tab"
				class:active={view === 'html'}
				aria-selected={view === 'html'}
				onclick={() => (view = 'html')}>HTML</button
			>
			<button
				type="button"
				role="tab"
				class:active={view === 'plain'}
				aria-selected={view === 'plain'}
				onclick={() => (view = 'plain')}>Plain text</button
			>
		</div>
	</div>

	<div class="subject-bar">
		<span class="subject-label">Subject:</span>
		<span class="subject-value">{subject || '—'}</span>
	</div>

	{#if error}
		<div class="preview-error">
			{error}
		</div>
	{:else if view === 'html'}
		<iframe
			title="Email HTML preview"
			srcdoc={htmlBody || '<div style="font-family:sans-serif;color:#999;padding:32px;text-align:center;">HTML-версия пуста — заполните поля или выберите шаблон с HTML-частью.</div>'}
			sandbox=""
		></iframe>
	{:else}
		<pre class="plain">{textBody || '— пусто —'}</pre>
	{/if}

	{#if loading}
		<div class="loading-overlay">
			<span class="spinner-small"></span>
			<span>Рендеринг…</span>
		</div>
	{/if}
</div>

<style>
	.preview {
		position: relative;
		display: flex;
		flex-direction: column;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 10px;
		overflow: hidden;
		min-height: 600px;
		transition: opacity var(--transition-fast);
	}

	.preview.dim {
		opacity: 0.7;
	}

	.preview-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 12px 16px;
		border-bottom: 1px solid var(--border-subtle);
		background: var(--bg);
	}

	.title {
		font-size: 12px;
		font-weight: 600;
		color: var(--text-secondary);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	.view-toggle {
		display: flex;
		gap: 4px;
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
		padding: 2px;
	}

	.view-toggle button {
		font-size: 12px;
		padding: 4px 10px;
		border: none;
		border-radius: 4px;
		background: transparent;
		color: var(--text-secondary);
		cursor: pointer;
	}

	.view-toggle button.active {
		background: var(--primary-subtle);
		color: var(--text-primary);
	}

	.subject-bar {
		display: flex;
		gap: 8px;
		align-items: baseline;
		padding: 10px 16px;
		font-size: 13px;
		background: var(--bg);
		border-bottom: 1px solid var(--border-subtle);
	}

	.subject-label {
		color: var(--text-muted);
		font-weight: 500;
		flex-shrink: 0;
	}

	.subject-value {
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	iframe {
		flex: 1;
		width: 100%;
		min-height: 500px;
		border: 0;
		background: #fff;
	}

	.plain {
		flex: 1;
		margin: 0;
		padding: 16px;
		font-family: var(--font-mono);
		font-size: 13px;
		line-height: 1.5;
		color: var(--text-primary);
		background: var(--bg);
		white-space: pre-wrap;
		overflow-y: auto;
	}

	.preview-error {
		padding: 16px;
		font-size: 13px;
		color: var(--status-critical);
		background: var(--bg);
	}

	.loading-overlay {
		position: absolute;
		top: 16px;
		right: 16px;
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 6px 12px;
		font-size: 12px;
		color: var(--text-secondary);
		background: var(--surface);
		border: 1px solid var(--border-subtle);
		border-radius: 16px;
		box-shadow: 0 2px 8px rgba(0, 0, 0, 0.2);
	}

	.spinner-small {
		display: inline-block;
		width: 12px;
		height: 12px;
		border: 2px solid var(--border);
		border-top-color: var(--primary);
		border-radius: 50%;
		animation: spin 0.7s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
