<script lang="ts">
	import { onMount } from 'svelte';
	import { describeApiError } from '$lib/api/client';
	import {
		fetchBillingOverview,
		fetchSubscription,
		fetchSubscriptions,
		issueSubscription,
		renewSubscription,
		retirePlan,
		revokeSubscription,
		savePlan,
		type Overview,
		type OrderStatus,
		type Plan,
		type Subscription,
		type SubscriptionDetail,
		type SubscriptionState,
	} from '$lib/api/billing';
	import { num, rub } from '$lib/product/format';

	// Subscriptions sold through the landing (internal/billing) and issued by
	// hand. Everything here is the owner's; revoking asks for a passkey.

	const STATE: Record<SubscriptionState, { label: string; cls: string }> = {
		live: { label: 'живая', cls: 'ok' },
		expired: { label: 'истекла', cls: 'warn' },
		spent: { label: 'трафик кончился', cls: 'warn' },
		revoked: { label: 'отозвана', cls: 'bad' },
	};
	const ORDER: Record<OrderStatus, { label: string; cls: string }> = {
		pending: { label: 'ждёт оплаты', cls: 'dim' },
		paid: { label: 'оплачен', cls: 'ok' },
		canceled: { label: 'отменён', cls: 'dim' },
		expired: { label: 'истёк', cls: 'dim' },
	};
	const GIB = 2 ** 30;

	let overview = $state<Overview | null>(null);
	let subs = $state<Subscription[]>([]);
	let query = $state('');
	let orderStatus = $state<OrderStatus | ''>('');
	let error = $state('');
	let loading = $state(false);

	let open = $state<string | null>(null);
	let detail = $state<SubscriptionDetail | null>(null);
	let renewPlan = $state('');
	let busy = $state(false);

	let issueName = $state('');
	let issuePlan = $state('');
	let issued = $state('');

	let draft = $state<Pick<Plan, 'id' | 'title' | 'days' | 'trafficGiB'> & { price: number }>({
		id: '',
		title: '',
		days: 30,
		trafficGiB: 1000,
		price: 0,
	});

	const onSale = $derived((overview?.plans ?? []).filter((p) => !p.retired));

	async function load() {
		loading = true;
		error = '';
		try {
			[overview, subs] = await Promise.all([fetchBillingOverview(orderStatus), fetchSubscriptions(query.trim())]);
			issuePlan ||= onSale[0]?.id ?? '';
		} catch (e) {
			error = describeApiError(e);
		} finally {
			loading = false;
		}
	}

	onMount(load);

	let searchTimer: ReturnType<typeof setTimeout> | undefined;
	function search() {
		clearTimeout(searchTimer);
		searchTimer = setTimeout(load, 300);
	}

	async function toggle(name: string) {
		if (open === name) {
			open = null;
			return;
		}
		open = name;
		detail = null;
		try {
			detail = await fetchSubscription(name);
			renewPlan = onSale.find((p) => p.id === detail?.subscription.plan)?.id ?? onSale[0]?.id ?? '';
		} catch (e) {
			error = describeApiError(e);
		}
	}

	async function act(fn: () => Promise<unknown>) {
		busy = true;
		error = '';
		try {
			await fn();
			await load();
			if (open) detail = await fetchSubscription(open);
		} catch (e) {
			error = describeApiError(e);
		} finally {
			busy = false;
		}
	}

	const renew = (name: string) => act(() => renewSubscription(name, renewPlan));

	function revoke(name: string) {
		if (!confirm(`Отозвать ${name}? Ссылка перестанет работать на всех нодах за минуту.`)) return;
		act(() => revokeSubscription(name));
	}

	function issue() {
		act(async () => {
			issued = (await issueSubscription(issueName.trim(), issuePlan)).url;
			issueName = '';
		});
	}

	function addPlan() {
		const { id, title, days, trafficGiB, price } = draft;
		act(async () => {
			await savePlan({ id: id.trim(), title: title.trim(), days, trafficGiB, priceCents: Math.round(price * 100) });
			draft = { id: '', title: '', days: 30, trafficGiB: 1000, price: 0 };
		});
	}

	const editPlan = (p: Plan) => act(() => savePlan(p));

	function retire(p: Plan) {
		if (!confirm(`Снять тариф «${p.title || p.id}» с продажи? Купленные подписки на нём доживут свой срок.`)) return;
		act(() => retirePlan(p.id));
	}

	const date = (s: string | null) =>
		s ? new Date(s).toLocaleDateString('ru', { day: '2-digit', month: '2-digit', year: '2-digit' }) : '—';
	const gb = (bytes: number) => num(bytes / GIB);
	const daysLeft = (s: string | null) => (s ? Math.ceil((new Date(s).getTime() - Date.now()) / 86_400_000) : null);
	const copy = (text: string) => navigator.clipboard.writeText(text);
</script>

<div class="head">
	<h1>подписки</h1>
	{#if overview}
		{#if overview.provider === ''}
			<span class="warn">оплата не подключена: лендинг показывает тарифы, но заказы не принимает</span>
		{:else if overview.provider === 'sandbox'}
			<span class="warn">sandbox: тестовые платежи, деньги не двигаются</span>
		{:else}
			<span class="dim">провайдер: {overview.provider}</span>
		{/if}
	{/if}
</div>

{#if error}<p class="bad">{error}</p>{/if}

{#if overview}
	<div class="strip">
		<span>живых подписок <b>{num(overview.totals.live)}</b></span>
		<span>оплачено за 30 дней <b>{rub(overview.totals.paid30dCents / 100)}</b></span>
		<span>заказов <b>{num(overview.totals.paid30dOrders)}</b></span>
	</div>
{/if}

<section class="card">
	<div class="top">
		<h2>подписки</h2>
		<input class="field" placeholder="имя или email" bind:value={query} oninput={search} />
		{#if loading}<span class="dim">обновляю…</span>{/if}
	</div>
	{#if subs.length === 0}
		<p class="empty">{query.trim() ? 'ничего не нашлось' : 'подписок пока нет'}</p>
	{:else}
		<table class="rows">
			<thead>
				<tr>
					<th>подписка</th><th>тариф</th><th>статус</th><th class="r">до</th><th class="r">трафик, ГиБ</th>
					<th class="r">ноды</th>
				</tr>
			</thead>
			<tbody>
				{#each subs as s (s.name)}
					<tr class="row" onclick={() => toggle(s.name)}>
						<td>
							<div class="mono">{s.name}</div>
							{#if s.email}<div class="dim">{s.email}</div>{/if}
						</td>
						<td class="mono">{s.plan || '—'}</td>
						<td class={STATE[s.state].cls}>{STATE[s.state].label}</td>
						<td class="r mono">
							{date(s.expiresAt)}
							{#if s.state === 'live' && (daysLeft(s.expiresAt) ?? 99) <= 7}
								<div class="warn">{daysLeft(s.expiresAt)} дн.</div>
							{/if}
						</td>
						<td class="r mono">{gb(s.usedBytes)}{s.trafficBytes ? ` / ${gb(s.trafficBytes)}` : ''}</td>
						<td class="r mono">{s.relays.length}</td>
					</tr>
					{#if open === s.name}
						<tr>
							<td colspan="6">
								<div class="detail">
									{#if !detail}
										<p class="dim">загружаю…</p>
									{:else}
										<div class="line">
											<span class="dim">ссылка</span>
											{#if detail.url}
												<code>{detail.url}</code>
												<button class="btn" onclick={() => copy(detail!.url)}>скопировать</button>
											{:else}
												<span class="dim">не хранится (отозвана или выдана до оплаты)</span>
											{/if}
										</div>
										<div class="line">
											<span class="dim">ноды</span>
											<span class="mono">{s.relays.join(', ') || 'не назначены'}</span>
										</div>
										<div class="line">
											<span class="dim">создана</span><span class="mono">{date(s.createdAt)}</span>
										</div>
										{#if s.state !== 'revoked'}
											<div class="line">
												<select class="field" bind:value={renewPlan} disabled={busy}>
													{#each onSale as p (p.id)}<option value={p.id}>{p.title || p.id}</option>{/each}
												</select>
												<button class="btn primary" disabled={busy || !renewPlan} onclick={() => renew(s.name)}>
													продлить бесплатно
												</button>
												<button class="btn danger" disabled={busy} onclick={() => revoke(s.name)}>отозвать</button>
											</div>
										{/if}
										{#if detail.orders.length}
											<table class="rows inner">
												<tbody>
													{#each detail.orders as o (o.id)}
														<tr>
															<td class="mono">{date(o.createdAt)}</td>
															<td>{o.renewal ? 'продление' : 'покупка'} · {o.plan}</td>
															<td class="r mono">{rub(o.amountCents / 100)}</td>
															<td class={ORDER[o.status].cls}>{ORDER[o.status].label}</td>
														</tr>
													{/each}
												</tbody>
											</table>
										{/if}
									{/if}
								</div>
							</td>
						</tr>
					{/if}
				{/each}
			</tbody>
		</table>
	{/if}
</section>

<section class="card">
	<h2>выдать вручную</h2>
	<p class="dim">подарок, тестер, компенсация. ссылка показывается здесь, на хабе она хранится зашифрованной</p>
	<div class="line">
		<input class="field" placeholder="имя: латиница, цифры, - и _" bind:value={issueName} />
		<select class="field" bind:value={issuePlan}>
			{#each onSale as p (p.id)}<option value={p.id}>{p.title || p.id}</option>{/each}
		</select>
		<button class="btn primary" disabled={busy || !issueName.trim() || !issuePlan} onclick={issue}>выдать</button>
	</div>
	{#if issued}
		<div class="line">
			<code>{issued}</code>
			<button class="btn" onclick={() => copy(issued)}>скопировать</button>
		</div>
	{/if}
</section>

{#if overview}
	<section class="card">
		<h2>тарифы</h2>
		<p class="dim">
			id тарифа = период на лендинге (1m, 6m, 12m). изменения не трогают уже купленные подписки. трафик — скрытый
			потолок на период против абуза
		</p>
		<table class="rows">
			<thead>
				<tr>
					<th>id</th><th>название</th><th class="r">дней</th><th class="r">ГиБ</th><th class="r">цена, ₽</th>
					<th class="r">подписок</th><th></th>
				</tr>
			</thead>
			<tbody>
				{#each overview.plans as p (p.id)}
					<tr class:retired={p.retired}>
						<td class="mono">{p.id}</td>
						{#if p.retired}
							<td>{p.title}</td>
							<td class="r mono">{p.days}</td>
							<td class="r mono">{p.trafficGiB}</td>
							<td class="r mono">{rub(p.priceCents / 100)}</td>
							<td class="r mono">{p.subscriptions}</td>
							<td class="dim">снят с продажи</td>
						{:else}
							<td><input class="field" bind:value={p.title} /></td>
							<td class="r"><input class="field num" type="number" min="1" bind:value={p.days} /></td>
							<td class="r"><input class="field num" type="number" min="0" bind:value={p.trafficGiB} /></td>
							<td class="r">
								<input
									class="field num"
									type="number"
									min="0"
									step="1"
									value={p.priceCents / 100}
									oninput={(e) => (p.priceCents = Math.round(Number(e.currentTarget.value) * 100))}
								/>
							</td>
							<td class="r mono">{p.subscriptions}</td>
							<td class="actions">
								<button class="btn" disabled={busy} onclick={() => editPlan(p)}>сохранить</button>
								<button class="btn danger" disabled={busy} onclick={() => retire(p)}>снять</button>
							</td>
						{/if}
					</tr>
				{/each}
				<tr>
					<td><input class="field" placeholder="id" bind:value={draft.id} /></td>
					<td><input class="field" placeholder="1 месяц" bind:value={draft.title} /></td>
					<td class="r"><input class="field num" type="number" min="1" bind:value={draft.days} /></td>
					<td class="r"><input class="field num" type="number" min="0" bind:value={draft.trafficGiB} /></td>
					<td class="r"><input class="field num" type="number" min="0" bind:value={draft.price} /></td>
					<td></td>
					<td class="actions">
						<button class="btn primary" disabled={busy || !draft.id.trim()} onclick={addPlan}>добавить</button>
					</td>
				</tr>
			</tbody>
		</table>
	</section>

	<section class="card">
		<div class="top">
			<h2>заказы</h2>
			<div class="chips">
				{#each [['', 'все'], ['paid', 'оплачены'], ['pending', 'ждут'], ['canceled', 'отменены'], ['expired', 'истекли']] as [v, label] (v)}
					<button
						class="chip"
						class:on={orderStatus === v}
						onclick={() => {
							orderStatus = v as OrderStatus | '';
							load();
						}}>{label}</button
					>
				{/each}
			</div>
		</div>
		{#if overview.orders.length === 0}
			<p class="empty">заказов нет</p>
		{:else}
			<table class="rows">
				<thead>
					<tr>
						<th>создан</th><th>email</th><th>что</th><th>подписка</th><th class="r">сумма</th><th>статус</th>
					</tr>
				</thead>
				<tbody>
					{#each overview.orders as o (o.id)}
						<tr>
							<td class="mono">{new Date(o.createdAt).toLocaleString('ru')}</td>
							<td>{o.email}</td>
							<td>{o.renewal ? 'продление' : 'покупка'} · <span class="mono">{o.plan}</span></td>
							<td class="mono">{o.account || '—'}</td>
							<td class="r mono">{rub(o.amountCents / 100)}</td>
							<td class={ORDER[o.status].cls}>{ORDER[o.status].label}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</section>
{/if}

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

	.top .field {
		max-width: 280px;
	}

	.top .chips {
		margin-left: auto;
	}

	.strip {
		display: flex;
		gap: 24px;
		margin-bottom: 16px;
		font-size: 12px;
		color: var(--text-2);
	}

	.row {
		cursor: pointer;
	}

	.row:hover td {
		background: oklch(0.21 0 0);
	}

	.r {
		width: 1%;
		white-space: nowrap;
		text-align: right;
	}

	.rows .dim {
		font-size: 11px;
	}

	.detail {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 12px;
		background: var(--inset);
		border: 1px solid var(--border-subtle);
		border-radius: 6px;
	}

	.line {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
		margin-top: 8px;
	}

	.detail .line {
		margin-top: 0;
	}

	.line .dim {
		min-width: 64px;
	}

	code {
		font-family: var(--mono);
		font-size: 11px;
		color: var(--text);
		overflow-wrap: anywhere;
	}

	select.field {
		max-width: 200px;
	}

	.num {
		width: 90px;
		text-align: right;
	}

	.actions {
		white-space: nowrap;
		text-align: right;
	}

	.retired td {
		color: var(--text-3);
	}

	.inner {
		margin-top: 4px;
	}

	.btn.danger {
		color: var(--bad);
	}

	.btn:disabled {
		opacity: 0.5;
		cursor: default;
	}
</style>
