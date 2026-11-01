<script lang="ts">
	// The support conversation as the team sees it — one renderer for every
	// page that shows a support DM (a ticket, the support inbox), so the same
	// thread can never read differently depending on where it was opened.
	//
	// Stateless about the wire: the page owns loading and posting, this owns
	// the draft, the scroll position and the look.
	import { tick } from 'svelte';
	import { mediaUrl } from '$lib/api/client';
	import type { AdminConvMessage } from '$lib/api/tickets';

	interface Props {
		messages: AdminConvMessage[];
		loading?: boolean;
		/** Name to fall back on for the user's rows. */
		userName?: string;
		/** The user's read marker — admin rows at or before it get the double tick. */
		userReadAt?: string | null;
		typing?: boolean;
		title?: string;
		/** Posts the draft. Resolves true when it landed, so the draft clears. */
		onSend: (text: string) => Promise<boolean>;
		onRefresh: () => void;
		onInput?: () => void;
	}

	let {
		messages,
		loading = false,
		userName = '',
		userReadAt = null,
		typing = false,
		title = 'Переписка с пользователем',
		onSend,
		onRefresh,
		onInput,
	}: Props = $props();

	let replyDraft = $state('');
	let sending = $state(false);
	let chatStreamEl: HTMLDivElement | undefined = $state();
	let showScrollDown = $state(false);
	let lastCount = 0;

	// Follow the thread down when it grows — unless the reader scrolled up to
	// look at history, where a jump would yank the line they are reading.
	$effect(() => {
		const count = messages.length;
		if (count === lastCount) return;
		const first = lastCount === 0;
		lastCount = count;
		if (first || !showScrollDown) {
			void tick().then(() => scrollToBottom(first ? 'instant' : 'smooth'));
		}
	});

	export function scrollToBottom(behavior: ScrollBehavior = 'smooth') {
		chatStreamEl?.scrollTo({ top: chatStreamEl.scrollHeight, behavior });
	}

	function handleChatScroll() {
		if (!chatStreamEl) return;
		const { scrollTop, scrollHeight, clientHeight } = chatStreamEl;
		showScrollDown = scrollHeight - scrollTop - clientHeight > 200;
	}

	async function send() {
		const text = replyDraft.trim();
		if (!text || sending) return;
		sending = true;
		try {
			if (await onSend(text)) replyDraft = '';
		} finally {
			sending = false;
		}
	}

	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
			e.preventDefault();
			void send();
		}
	}

	function fmtReplyTime(iso: string): string {
		const d = new Date(iso);
		if (isNaN(d.getTime())) return iso;
		return d.toLocaleString('ru-RU', {
			day: '2-digit',
			month: '2-digit',
			year: 'numeric',
			hour: '2-digit',
			minute: '2-digit',
		});
	}
</script>

<section class="card chat-card">
	<header class="chat-head">
		<div>
			<h3>{title}</h3>
			<p class="chat-sub">чат с «Hikkari» — то, что пользователь видит в своём списке чатов</p>
		</div>
		<button class="icon-btn" onclick={onRefresh} disabled={loading} title="обновить">
			<svg
				width="14"
				height="14"
				viewBox="0 0 24 24"
				fill="none"
				stroke="currentColor"
				stroke-width="2"
				stroke-linecap="round"
				stroke-linejoin="round"
			>
				<polyline points="23 4 23 10 17 10" />
				<polyline points="1 20 1 14 7 14" />
				<path d="M3.51 9a9 9 0 0114.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0020.49 15" />
			</svg>
		</button>
	</header>

	<div class="chat-stream-container">
		<div class="chat-stream" bind:this={chatStreamEl} onscroll={handleChatScroll}>
			{#each messages as m (m.id)}
				{#if m.authorKind === 'system'}
					<div class="bubble-row system">
						<div class="bubble system-bubble">
							<svg
								width="12"
								height="12"
								viewBox="0 0 24 24"
								fill="none"
								stroke="currentColor"
								stroke-width="2"
								stroke-linecap="round"
								><circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line
									x1="12"
									y1="16"
									x2="12.01"
									y2="16"
								/></svg
							>
							<span>{m.content}</span>
							<time>{fmtReplyTime(m.createdAt)}</time>
						</div>
					</div>
				{:else}
					<div class="bubble-row" class:them={m.authorKind === 'user'} class:me={m.authorKind === 'admin'}>
						{#if m.authorKind === 'user'}
							<div class="bubble-avatar them">
								{((m.senderUsername || userName || '?')[0] || '?').toUpperCase()}
							</div>
						{/if}
						<div class="bubble" class:bubble-them={m.authorKind === 'user'} class:bubble-me={m.authorKind === 'admin'}>
							<div class="bubble-author">
								{#if m.authorKind === 'admin'}
									команда поддержки
								{:else}
									{m.senderUsername || userName || 'пользователь'}
								{/if}
							</div>
							{#if m.attachmentKey && m.attachmentType === 'image'}
								<a class="bubble-image" href={mediaUrl(m.attachmentKey)} target="_blank" rel="noreferrer">
									<img src={mediaUrl(m.attachmentKey)} alt={m.fileName || ''} loading="lazy" />
								</a>
							{:else if m.attachmentKey}
								<a class="bubble-attach" href={mediaUrl(m.attachmentKey)} target="_blank" rel="noreferrer">
									<svg
										width="14"
										height="14"
										viewBox="0 0 24 24"
										fill="none"
										stroke="currentColor"
										stroke-width="2"
										stroke-linecap="round"
										><path
											d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"
										/></svg
									>
									{m.fileName || m.attachmentType || 'вложение'}
								</a>
							{/if}
							{#if m.content}
								<p class="bubble-text">{m.content}</p>
							{/if}
							<time class="bubble-time">
								{fmtReplyTime(m.createdAt)}
								{#if m.authorKind === 'admin' && userReadAt && new Date(m.createdAt) <= new Date(userReadAt)}
									<!-- Double tick once the user's read marker has
										 passed this row — the same thing they see on
										 their own outgoing messages. -->
									<span class="read-tick" title="прочитано">
										<svg
											width="12"
											height="12"
											viewBox="0 0 18 14"
											fill="none"
											stroke="currentColor"
											stroke-width="2"
											stroke-linecap="round"
											stroke-linejoin="round"
											><polyline points="1 8 5 12 12 5" /><polyline points="7 8 11 12 17 5" /></svg
										>
									</span>
								{/if}
							</time>
						</div>
						{#if m.authorKind === 'admin'}
							<div class="bubble-avatar me" title="команда поддержки">SU</div>
						{/if}
					</div>
				{/if}
			{/each}
			{#if messages.length === 0 && !loading}
				<div class="empty-conv">
					<svg
						width="40"
						height="40"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="1.5"
						stroke-linecap="round"><path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z" /></svg
					>
					<p>сообщений ещё нет</p>
					<span>отправьте ответ — переписка появится у пользователя в чате с «Hikkari»</span>
				</div>
			{/if}
			{#if typing}
				<div class="typing-indicator" aria-live="polite">
					<span class="typing-dots"><span></span><span></span><span></span></span>
					<span>пользователь печатает…</span>
				</div>
			{/if}
		</div>

		{#if showScrollDown}
			<button class="scroll-down-btn" onclick={() => scrollToBottom()} aria-label="промотать вниз">
				<svg
					width="18"
					height="18"
					viewBox="0 0 24 24"
					fill="none"
					stroke="currentColor"
					stroke-width="2.5"
					stroke-linecap="round"
					stroke-linejoin="round"><polyline points="6 9 12 15 18 9" /></svg
				>
			</button>
		{/if}
	</div>

	<div class="reply-box">
		<textarea
			id="reply-input"
			bind:value={replyDraft}
			oninput={onInput}
			onkeydown={onKeydown}
			rows="3"
			class="reply-input"
			placeholder="Объясните, что произошло, или попросите подробности…"
			maxlength="4000"
		></textarea>
		<div class="reply-actions">
			<span class="char-count" class:warn={replyDraft.length > 3500}>{replyDraft.length}/4000</span>
			<button class="btn primary" onclick={send} disabled={sending || replyDraft.trim().length === 0}>
				{#if sending}
					Отправка…
				{:else}
					Отправить
					<svg
						width="12"
						height="12"
						viewBox="0 0 24 24"
						fill="none"
						stroke="currentColor"
						stroke-width="2.5"
						stroke-linecap="round"
						stroke-linejoin="round"
						><line x1="22" y1="2" x2="11" y2="13" /><polygon points="22 2 15 22 11 13 2 9 22 2" /></svg
					>
				{/if}
			</button>
		</div>
	</div>
</section>

<style>
	.card {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 12px;
		padding: 18px 20px;
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.card h3 {
		margin: 0;
		font-size: 14px;
		font-weight: 600;
		color: var(--text-primary);
		letter-spacing: -0.01em;
	}

	.btn {
		padding: 8px 16px;
		border-radius: 8px;
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--text-primary);
		font: inherit;
		font-size: 12px;
		font-weight: 600;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		gap: 8px;
		transition: all var(--transition-fast);
	}
	.btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.btn.primary {
		background: var(--primary);
		color: var(--bg);
		border-color: var(--primary);
	}
	.btn.primary:hover:not(:disabled) {
		background: var(--primary-hover);
		border-color: var(--primary-hover);
	}

	.icon-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 30px;
		height: 30px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--text-muted);
		cursor: pointer;
		transition: all var(--transition-fast);
	}
	.icon-btn:hover:not(:disabled) {
		background: var(--surface-hover);
		color: var(--text-primary);
		border-color: var(--text-muted);
	}
	.icon-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.chat-card {
		gap: 0;
		padding: 0;
		min-height: 600px;
		overflow: hidden;
	}

	.chat-head {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 12px;
		padding: 16px 20px;
		border-bottom: 1px solid var(--border);
		background: var(--bg-raised);
	}
	.chat-sub {
		margin: 4px 0 0;
		font-size: 11px;
		color: var(--text-muted);
		line-height: 1.4;
	}

	.chat-stream-container {
		position: relative;
		flex: 1;
		display: flex;
		flex-direction: column;
		min-height: 400px;
		background: var(--bg);
	}

	.chat-stream {
		flex: 1;
		display: flex;
		flex-direction: column;
		gap: 12px;
		padding: 20px;
		max-height: 70vh;
		overflow-y: auto;
		scroll-behavior: smooth;
	}

	.scroll-down-btn {
		position: absolute;
		right: 20px;
		bottom: 20px;
		width: 36px;
		height: 36px;
		border-radius: 50%;
		background: var(--surface-active);
		border: 1px solid var(--border);
		color: var(--text-primary);
		display: flex;
		align-items: center;
		justify-content: center;
		cursor: pointer;
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
		transition: all var(--transition-fast);
		z-index: 10;
	}
	.scroll-down-btn:hover {
		background: var(--primary);
		color: var(--bg);
		border-color: var(--primary);
	}

	.bubble-row {
		display: flex;
		gap: 10px;
		max-width: 100%;
		align-items: flex-end;
	}
	.bubble-row.them {
		justify-content: flex-start;
	}
	.bubble-row.me {
		justify-content: flex-end;
	}
	.bubble-row.system {
		justify-content: center;
		margin: 8px 0;
	}

	.bubble-avatar {
		width: 28px;
		height: 28px;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 11px;
		font-weight: 700;
		flex-shrink: 0;
	}
	.bubble-avatar.them {
		background: var(--surface-active);
		color: var(--text-secondary);
		border: 1px solid var(--border);
	}
	.bubble-avatar.me {
		background: var(--primary);
		color: var(--bg);
	}

	.bubble {
		max-width: 80%;
		padding: 10px 14px;
		border-radius: 12px;
		display: flex;
		flex-direction: column;
		gap: 4px;
		position: relative;
		font-size: 13px;
	}
	.bubble-them {
		background: var(--bubble-them);
		border: 1px solid var(--border);
		border-bottom-left-radius: 2px;
		color: var(--text-primary);
	}
	.bubble-me {
		background: var(--bubble-own);
		border: 1px solid var(--border);
		border-bottom-right-radius: 2px;
		color: var(--text-primary);
	}

	.bubble-author {
		font-size: 10px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
	}
	.bubble-me .bubble-author {
		color: var(--primary);
	}

	.bubble-text {
		margin: 0;
		line-height: 1.5;
		white-space: pre-wrap;
		word-break: break-word;
	}

	.bubble-time {
		font-size: 9px;
		color: var(--text-muted);
		font-family: var(--font-mono);
		align-self: flex-end;
		margin-top: 2px;
	}

	.bubble-image {
		display: block;
		border-radius: 6px;
		overflow: hidden;
		max-width: 320px;
		border: 1px solid var(--border);
	}
	.bubble-image img {
		display: block;
		max-width: 100%;
		max-height: 240px;
		object-fit: contain;
	}

	.bubble-attach {
		display: inline-flex;
		gap: 8px;
		align-items: center;
		padding: 8px 12px;
		border-radius: 6px;
		background: var(--bg);
		border: 1px solid var(--border-subtle);
		font-size: 12px;
		color: var(--text-secondary);
		text-decoration: none;
		max-width: 260px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.bubble-attach:hover {
		color: var(--text-primary);
		border-color: var(--text-muted);
	}

	.system-bubble {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 6px 14px;
		border-radius: 999px;
		background: var(--bg-raised);
		border: 1px dashed var(--border);
		color: var(--text-secondary);
		font-size: 11px;
		max-width: 90%;
	}
	.system-bubble svg {
		color: var(--text-muted);
		flex-shrink: 0;
	}
	.system-bubble time {
		color: var(--text-muted);
		font-family: var(--font-mono);
		font-size: 9px;
		margin-left: 6px;
	}

	.empty-conv {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 12px;
		padding: 60px 20px;
		text-align: center;
		color: var(--text-muted);
		flex: 1;
	}
	.empty-conv svg {
		opacity: 0.3;
	}
	.empty-conv p {
		margin: 0;
		font-size: 13px;
		font-weight: 600;
		color: var(--text-secondary);
	}
	.empty-conv span {
		font-size: 11px;
		max-width: 280px;
		line-height: 1.5;
	}

	.typing-indicator {
		display: inline-flex;
		align-items: center;
		gap: 8px;
		padding: 4px 10px;
		font-size: 11px;
		color: var(--text-muted);
		font-style: italic;
		align-self: flex-start;
	}
	.typing-dots {
		display: inline-flex;
		gap: 3px;
	}
	.typing-dots span {
		width: 3px;
		height: 3px;
		border-radius: 50%;
		background: currentColor;
		opacity: 0.4;
		animation: typing-pulse 1.2s infinite ease-in-out;
	}
	.typing-dots span:nth-child(2) {
		animation-delay: 0.2s;
	}
	.typing-dots span:nth-child(3) {
		animation-delay: 0.4s;
	}
	@keyframes typing-pulse {
		0%,
		60%,
		100% {
			opacity: 0.3;
			transform: scale(1);
		}
		30% {
			opacity: 1;
			transform: scale(1.2);
		}
	}

	.read-tick {
		display: inline-flex;
		vertical-align: middle;
		margin-left: 4px;
		color: var(--primary);
	}

	.reply-box {
		display: flex;
		flex-direction: column;
		gap: 12px;
		padding: 16px 20px;
		border-top: 1px solid var(--border);
		background: var(--bg-raised);
	}
	.reply-input {
		width: 100%;
		padding: 12px;
		border-radius: 10px;
		border: 1px solid var(--border);
		background: var(--bg);
		color: var(--text-primary);
		font: inherit;
		font-size: 13px;
		line-height: 1.5;
		resize: vertical;
		min-height: 80px;
		transition: border-color var(--transition-fast);
	}
	.reply-input:focus {
		outline: none;
		border-color: var(--text-muted);
	}
	.reply-actions {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 8px;
	}
	.char-count {
		font-size: 10px;
		color: var(--text-muted);
		font-family: var(--font-mono);
	}
	.char-count.warn {
		color: var(--status-warning);
	}

	/* ── SIDE ─────────────────────────────────────────────────────── */
</style>
