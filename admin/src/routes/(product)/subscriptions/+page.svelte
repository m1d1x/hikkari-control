<script lang="ts">
	import { onMount } from 'svelte';
	import { describeApiError } from '$lib/api/client';
	import {
		fetchBillingOverview,
		fetchSubscription,
		fetchSubscriptions,
		issueSubscription,
		issueTesterSubscription,
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
	import {
		fetchTesterInstalls,
		fetchTesterRelays,
		renameTester,
		type TesterInstall,
		type TesterRelays,
	} from '$lib/api/support';
	import { fetchProductDowntime } from '$lib/api/product';
	import { ago, num, rub } from '$lib/product/format';

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

	// Testers are the hub's users today: installs of the client in tester mode,
	// not subscriptions sold or issued, so they come from the tester stream.
	let installs = $state<TesterInstall[]>([]);
	let rounds = $state<Record<string, TesterRelays>>({});
	// Minutes the tunnel was on and carried nothing, last 24 h (ROADMAP 3.3).
	let lost = $state<Record<string, number>>({});

	async function loadTesters() {
		try {
			const [list, relays, down] = await Promise.all([
				fetchTesterInstalls(),
				fetchTesterRelays('24h'),
				fetchProductDowntime('24h').catch(() => null),
			]);
			installs = [...list.installs].sort((a, b) => Number(b.online) - Number(a.online) || b.last_seen.localeCompare(a.last_seen));
			rounds = Object.fromEntries(relays.installs.map((r) => [r.install_id, r]));
			lost = Object.fromEntries((down?.people ?? []).map((p) => [p.install, p.lostTotal]));
		} catch {
			// the subscriptions below still load; a missing tester list is not an error of theirs
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
			await loadTesters();
		} catch (e) {
			error = describeApiError(e);
		}
	}

	const alive = (id: string) => rounds[id]?.relays.filter((r) => r.up).length ?? null;

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
	let testerPlan = $state('');
	let tester = $state<{ name: string; url: string } | null>(null);

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
			testerPlan ||= onSale[0]?.id ?? '';
		} catch (e) {
			error = describeApiError(e);
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		load();
		loadTesters();
	});

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

	function issueTester() {
		act(async () => {
			tester = await issueTesterSubscription(testerPlan);
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
		s ? new Date(s).toLocaleDateString('ru', { day: '2-digit', month: '2-digit', year: '2-digit' }) : '-';
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

<section class="card tester">
	<div class="line">
		<button class="btn primary" disabled={busy || !testerPlan} onclick={issueTester}>
			сгенерировать ссылку для тестера
		</button>
		<select class="field" bind:value={testerPlan} disabled={busy}>
			{#each onSale as p (p.id)}<option value={p.id}>{p.title || p.id}</option>{/each}
		</select>
	</div>
	{#if tester}
		<div class="line">
			<span class="mono">{tester.name}</span>
			<code>{tester.url}</code>
			<button class="btn" onclick={() => copy(tester!.url)}>скопировать</button>
		</div>
	{/if}
</section>

<section class="card">
	<div class="top">
		<h2>тестеры</h2>
	</div>
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
						<td class="mono" class:dim={!lost[i.install_id]} title="простой за 24 ч">
							{#if lost[i.install_id] === undefined}-{:else}{num(lost[i.install_id])} мин простоя{/if}
						</td>
						<td class="r mono dim">{ago(new Date(i.last_seen).getTime())}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</section>

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
							<div class="mono">
								{s.name}{#if s.tester}<span class="tag">тестер</span>{/if}
							</div>
							{#if s.email}<div class="dim">{s.email}</div>{/if}
						</td>
						<td class="mono">{s.plan || '-'}</td>
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
							<td class="mono">{o.account || '-'}</td>
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
	.testers {
		table-layout: fixed;
	}

	.testers td:nth-child(1) {
		width: 28%;
	}

	.testers td:nth-child(2) {
		width: 20%;
	}

	.testers td:nth-child(3) {
		width: 22%;
	}

	.testers td:nth-child(4) {
		width: 18%;
	}

	.testers td:nth-child(5) {
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

	.tag {
		margin-left: 8px;
		padding: 1px 6px;
		font-family: var(--sans, inherit);
		font-size: 10px;
		color: var(--text-2);
		border: 1px solid var(--border-subtle);
		border-radius: 4px;
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
