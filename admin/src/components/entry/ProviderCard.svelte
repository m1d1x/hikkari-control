<script lang="ts">
	import TimeSeries from '$components/entry/ui/TimeSeries.svelte';
	import {
		saveProvider,
		checkProvider,
		previewProviderCatalog,
		type ProviderCatalog,
		describeProviderError,
		formatCents,
		type HostingCatalog,
		type ProviderAccount,
		type ProviderStats,
		type DomainSpend,
	} from '$lib/api/entry';

	interface Props {
		account: ProviderAccount;
		onchange: (a: ProviderAccount) => void;
		catalog?: HostingCatalog | null;
		stats?: ProviderStats;
		domainSpend?: DomainSpend | null;
	}

	let { account, onchange, catalog = null, stats, domainSpend = null }: Props = $props();

	let enabled = $state(false);
	let creds = $state<Record<string, string>>({});
	let tlds = $state('');
	let region = $state('');
	let plan = $state('');
	let seededFrom = '';
	let draftCatalog = $state<ProviderCatalog | null>(null);
	let catalogLoading = $state(false);
	let catalogError = $state<string | null>(null);

	let drawerOpen = $state(false);
	let busy = $state(false);
	let error = $state<string | null>(null);
	let note = $state<string | null>(null);

	$effect(() => {
		const stamp = `${account.kind}:${account.updatedAt ?? ''}:${account.configured}`;
		if (stamp === seededFrom) return;
		seededFrom = stamp;
		enabled = account.enabled;
		creds = Object.fromEntries(account.fields.map((f) => [f.name, '']));
		tlds = account.tlds.join(', ');
		region = account.defaultRegion ?? '';
		plan = account.defaultPlan ?? '';
		draftCatalog = null;
		catalogError = null;
	});

	const hosting = $derived(account.role === 'hosting');
	const typedAny = $derived(Object.values(creds).some((v) => v.trim() !== ''));
	const typedAll = $derived(account.fields.every((f) => (creds[f.name] ?? '').trim() !== ''));
	// A different hoster's active catalog must never populate this account.
	const offer = $derived(draftCatalog ?? (!typedAny && catalog?.kind === account.kind ? catalog : null));
	const regions = $derived(
		[...(offer?.regions ?? [])].sort((a, b) => a.country.localeCompare(b.country) || a.city.localeCompare(b.city)),
	);
	const plansHere = $derived(
		(offer?.plans ?? [])
			.filter((p) => p.regions.includes(region) && p.monthlyCents > 0)
			.sort((a, b) => a.monthlyCents - b.monthlyCents || a.id.localeCompare(b.id)),
	);
	const selectionValid = $derived(
		!!offer && regions.some((r) => r.id === region) && plansHere.some((p) => p.id === plan),
	);

	const check = $derived(account.check);

	const balanceCents = $derived(check?.balanceCents ?? null);
	const providerLimitCents = $derived(check?.accountLimitCents ?? null);

	// The price of a terabyte is the default plan's own: its monthly price over
	// the traffic it includes, both from the hoster's catalog. A hard-coded
	// quota per provider and the owner's spending cap standing in for a price
	// read "$10 per TB" for an account that had bought nothing (stand, 25.10).
	// No plan in the catalog, no figure.
	const defaultPlan = $derived((offer?.plans ?? []).find((p) => p.id === (account.defaultPlan ?? '')) ?? null);
	const trafficGb = $derived(defaultPlan && defaultPlan.bandwidthGb > 0 ? defaultPlan.bandwidthGb : null);
	const boxCents = $derived(defaultPlan && defaultPlan.monthlyCents > 0 ? defaultPlan.monthlyCents : null);
	const costPerTbDollars = $derived(
		trafficGb !== null && boxCents !== null ? boxCents / 100 / (trafficGb / 1000) : null,
	);

	function formatTraffic(gb: number): string {
		if (gb >= 1000) {
			const tb = gb / 1000;
			return `${Number.isInteger(tb) ? tb : tb.toFixed(1)} ТБ`;
		}
		return `${gb} ГБ`;
	}

	const months = ['янв', 'фев', 'мар', 'апр', 'май', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'];

	function dayLabel(iso: string): string {
		const p = iso.split('-');
		return p.length === 3 ? `${parseInt(p[2], 10)} ${months[parseInt(p[1], 10) - 1] ?? p[1]}` : iso;
	}

	function credentialsChanged(): void {
		draftCatalog = null;
		catalogError = null;
	}

	function selectRegion(value: string): void {
		region = value;
		if (!offer?.plans.some((p) => p.id === plan && p.regions.includes(value))) plan = '';
	}

	async function loadCatalog(): Promise<void> {
		if ((typedAny && !typedAll) || (!account.configured && !typedAll)) {
			catalogError = 'Заполни все поля ключа';
			return;
		}
		catalogLoading = true;
		catalogError = null;
		draftCatalog = null;
		try {
			draftCatalog = (await previewProviderCatalog(account.kind, typedAny ? { ...creds } : undefined)).catalog;
			if (!draftCatalog.regions.some((r) => r.id === region)) region = '';
			if (!draftCatalog.plans.some((p) => p.id === plan && p.regions.includes(region))) plan = '';
		} catch (e) {
			catalogError = describeProviderError(e);
		} finally {
			catalogLoading = false;
		}
	}

	$effect(() => {
		if (drawerOpen && hosting && account.configured && !typedAny && !offer && !catalogLoading && !catalogError && !busy)
			void loadCatalog();
	});

	async function toggleEnabled() {
		if (!account.configured) return;
		if (!enabled && hosting && !offer) await loadCatalog();
		if (!enabled && hosting && !selectionValid) {
			drawerOpen = true;
			return;
		}
		enabled = !enabled;
		if (!(await onSave())) enabled = account.enabled;
	}

	async function onSave(): Promise<boolean> {
		error = note = null;
		if (!account.configured && !typedAll) {
			error = 'заполни все поля ключа';
			return false;
		}
		if (typedAny && !typedAll) {
			error = 'новый ключ сохраняется целиком, заполни все поля';
			return false;
		}
		if (hosting && (!account.configured || enabled) && !selectionValid) {
			error = 'Загрузи каталог и выбери доступные регион и тариф';
			return false;
		}
		busy = true;
		try {
			const res = await saveProvider(account.kind, {
				enabled: !account.configured || enabled,
				credentials: typedAny ? { ...creds } : undefined,
				tlds: hosting
					? []
					: tlds
							.split(/[\s,]+/)
							.map((t) => t.trim())
							.filter(Boolean),
				defaultRegion: hosting ? region.trim() : undefined,
				defaultPlan: hosting ? plan.trim() : undefined,
			});
			note = typedAny ? 'ключ проверен и сохранён' : 'сохранено';
			onchange(res.provider);
			return true;
		} catch (e) {
			error = describeProviderError(e);
			return false;
		} finally {
			busy = false;
		}
	}

	async function onCheck() {
		error = note = null;
		busy = true;
		try {
			const res = await checkProvider(account.kind);
			onchange(res.provider);
			note = res.provider.check?.ok ? 'проверка прошла' : 'проверка не прошла';
		} catch (e) {
			error = describeProviderError(e);
		} finally {
			busy = false;
		}
	}
</script>

{#snippet hostingFields()}
	<div class="catalog-actions">
		<button
			class="btn"
			onclick={loadCatalog}
			disabled={busy || catalogLoading || (!account.configured && !typedAll) || (typedAny && !typedAll)}
		>
			{catalogLoading ? 'Загружаю регионы и тарифы…' : 'Проверить ключ и загрузить регионы и тарифы'}
		</button>
		{#if catalogError}<p class="err-msg" role="alert">{catalogError}</p>{/if}
		{#if offer && regions.length === 0}<p class="err-msg">
				Провайдер не вернул доступных регионов. Попробуй обновить каталог.
			</p>{/if}
	</div>
	<label class="field">
		<span>Регион размещения</span>
		<select
			value={region}
			onchange={(event) => selectRegion(event.currentTarget.value)}
			disabled={busy || catalogLoading || !offer || regions.length === 0}
		>
			<option value="">{offer ? 'Выбери регион' : 'Сначала загрузи каталог по ключу'}</option>
			{#each regions as r (r.id)}<option value={r.id}>{r.city}, {r.country}</option>{/each}
		</select>
	</label>
	<label class="field">
		<span>Тариф машины</span>
		<select bind:value={plan} disabled={busy || catalogLoading || !region || plansHere.length === 0}>
			<option value=""
				>{!region
					? 'Сначала выбери регион'
					: plansHere.length
						? 'Выбери тариф'
						: 'В этом регионе нет доступных тарифов'}</option
			>
			{#each plansHere as p (p.id)}
				<option value={p.id}
					>{formatCents(p.monthlyCents)}/мес · {p.vcpus} CPU · {(p.ramMb / 1024).toLocaleString('ru-RU')} ГБ RAM · {p.diskGb}
					ГБ SSD · {formatTraffic(p.bandwidthGb)} трафика</option
				>
			{/each}
		</select>
	</label>
{/snippet}

{#if !account.configured && (!stats || stats.boughtCount === 0)}
	<!-- Compact item for unconnected provider with no fleet -->
	<div class="prov prov-compact" class:prov-hosting={hosting} class:prov-registrar={!hosting}>
		<div class="prov-head compact-head">
			<span class="pname m">{account.title || account.kind}</span>
			{#if hosting}
				<span class="badge badge-hosting">VPS · серверы</span>
			{:else}
				<span class="badge badge-registrar">Домены · DNS</span>
			{/if}
			<span class="state st-off">не подключен</span>
			<div class="right">
				<button class="btn btn-sm" onclick={() => (drawerOpen = !drawerOpen)}>
					{drawerOpen ? '− скрыть' : '+ подключить'}
				</button>
			</div>
		</div>

		{#if drawerOpen}
			<div class="config">
				<div class="fields">
					{#each account.fields as f (f.name)}
						<label class="field">
							<span>{f.label}</span>
							<input
								type={f.secret ? 'password' : 'text'}
								class="mono"
								bind:value={creds[f.name]}
								oninput={credentialsChanged}
								disabled={busy || catalogLoading}
							/>
						</label>
					{/each}

					{#if hosting}
						{@render hostingFields()}
					{:else}
						<label class="field" style="grid-column: span 2">
							<span>порядок зон покупки</span>
							<input
								bind:value={tlds}
								placeholder="lol, top, mom, autos"
								class="mono"
								disabled={busy || catalogLoading}
							/>
						</label>
					{/if}
				</div>

				{#if error}<p class="err-msg">{error}</p>{/if}
				{#if note}<p class="note-msg">{note}</p>{/if}

				<div class="cfg-acts">
					<button class="btn prim" onclick={onSave} disabled={busy || catalogLoading}>
						{busy ? 'сохраняю…' : 'сохранить и подключить'}
					</button>
				</div>
			</div>
		{/if}
	</div>
{:else}
	<!-- Full card for provider: stats open immediately -->
	<div class="prov" class:prov-hosting={hosting} class:prov-registrar={!hosting}>
		<div class="prov-head">
			<span class="pname m">{account.title || account.kind}</span>
			{#if hosting}
				<span class="badge badge-hosting">VPS · серверы</span>
			{:else}
				<span class="badge badge-registrar">Домены · DNS</span>
			{/if}

			{#if check && !check.ok}
				<span class="state st-bad">ошибка ключа</span>
			{/if}

			<div class="right">
				<span class="toggle-label">автопокупка</span>
				<button
					class="toggle-btn"
					class:on={enabled}
					onclick={toggleEnabled}
					disabled={busy || catalogLoading}
					aria-label="Включить покупку через {account.kind}"
				>
					<span class="toggle-knob"></span>
				</button>
			</div>
		</div>

		<!-- Immediate stats: open directly on page load -->
		<div class="pstats">
			{#if hosting}
				<div class="pcell">
					<span class="label">баланс</span>
					<div class="value m" class:win-bad={balanceCents !== null && balanceCents < 500}>
						{balanceCents !== null ? formatCents(balanceCents) : account.configured ? '—' : 'нет данных'}
					</div>
					<span class="sub">
						{#if providerLimitCents !== null && providerLimitCents > 0}
							лимит {formatCents(providerLimitCents)}/мес
						{:else}
							потолки трат в автопилоте
						{/if}
					</span>
				</div>

				<div class="pcell">
					<span class="label">машин куплено</span>
					<div class="value m">{stats?.boughtCount ?? 0}</div>
					<span class="sub">
						{stats?.aliveCount ?? 0} в работе · {stats?.bannedCount ?? 0} в бане
					</span>
				</div>

				<div class="pcell">
					<span class="label">расход / сутки</span>
					<div class="value m">{formatCents(Math.round((stats?.monthlyCents ?? 0) / 30))}</div>
					<span class="sub">{formatCents(stats?.monthlyCents ?? 0)} / мес</span>
				</div>

				<div class="pcell">
					<span class="label">себестоимость 1 ТБ</span>
					<div class="value m">
						{#if costPerTbDollars !== null}
							${costPerTbDollars.toFixed(2)}
						{:else}
							—
						{/if}
					</div>
					<span class="sub">
						{#if defaultPlan && trafficGb !== null && boxCents !== null}
							{defaultPlan.id}: {formatTraffic(trafficGb)} · {formatCents(boxCents)}/мес
						{:else}
							тариф по умолчанию не выбран
						{/if}
					</span>
				</div>
			{:else}
				<div class="pcell">
					<span class="label">баланс</span>
					<div class="value m" class:win-bad={balanceCents !== null && balanceCents < 500}>
						{balanceCents !== null ? formatCents(balanceCents) : account.configured ? '—' : 'нет данных'}
					</div>
					<span class="sub">
						{#if providerLimitCents !== null && providerLimitCents > 0}
							лимит {formatCents(providerLimitCents)}/мес
						{:else}
							потолки трат в автопилоте
						{/if}
					</span>
				</div>

				<div class="pcell">
					<span class="label">расход в месяц</span>
					<div class="value m">{domainSpend ? formatCents(domainSpend.monthCents) : '—'}</div>
					<span class="sub">из {domainSpend ? formatCents(domainSpend.monthlyCapCents) : '—'} на все домены</span>
				</div>

				<div class="pcell">
					<span class="label">расход сегодня</span>
					<div class="value m">{domainSpend ? formatCents(domainSpend.dayCents) : '—'}</div>
					<span class="sub">из {domainSpend ? formatCents(domainSpend.dailyCapCents) : '—'} в день</span>
				</div>

				<div class="pcell">
					<span class="label">зоны</span>
					<div class="value m" style="font-size: 14px;">
						{account.tlds.length > 0 ? account.tlds.join(', ') : 'все'}
					</div>
					<span class="sub">в ротации</span>
				</div>
			{/if}
		</div>

		<!-- TimeSeries Chart directly on the card: line chart with area fill -->
		{#if hosting}
			<div class="chart-block">
				<div class="chart-head">
					<span class="ch-label">баны адресов за 30 дней</span>
					<span class="ch-val m" class:bad={(stats?.bansWindow ?? 0) > 0}>
						{stats?.bansWindow ?? '—'}
						{stats?.bansWindow === 1
							? 'бан'
							: (stats?.bansWindow ?? 0) >= 2 && (stats?.bansWindow ?? 0) <= 4
								? 'бана'
								: 'банов'}
					</span>
				</div>
				<TimeSeries
					labels={(stats?.bansDaily ?? []).map((d) => dayLabel(d.day))}
					series={[
						{
							name: 'баны',
							color: (stats?.bansWindow ?? 0) > 0 ? '#f43f5e' : '#10b981',
							vals: (stats?.bansDaily ?? []).map((d) => d.bans),
							type: 'line',
							area: true,
						},
					]}
					height="72px"
					showAxis={false}
					emptyNote="нет измерений банов за окно"
				/>
			</div>
		{/if}

		{#if check && !check.ok && check.error}
			<div class="check-err">{check.error}</div>
		{/if}

		<!-- Drawer toggle for setup -->
		<button class="cfg-toggle" onclick={() => (drawerOpen = !drawerOpen)}>
			<span class="caret">{drawerOpen ? '−' : '+'}</span>
			настройка: ключ, {hosting ? 'регион и тариф' : 'порядок зон'}
		</button>

		<!-- Config drawer -->
		{#if drawerOpen}
			<div class="config">
				<div class="fields">
					{#each account.fields as f (f.name)}
						<label class="field">
							<span>{f.label}</span>
							<input
								type={f.secret ? 'password' : 'text'}
								class="mono"
								bind:value={creds[f.name]}
								oninput={credentialsChanged}
								placeholder={f.secret ? 'сохранён' : (account.credsHint ?? 'сохранён')}
								disabled={busy || catalogLoading}
							/>
						</label>
					{/each}

					{#if hosting}
						{@render hostingFields()}
					{:else}
						<label class="field" style="grid-column: span 2">
							<span>порядок зон покупки</span>
							<input
								bind:value={tlds}
								placeholder="lol, top, mom, autos"
								class="mono"
								disabled={busy || catalogLoading}
							/>
						</label>
					{/if}
				</div>

				{#if error}<p class="err-msg">{error}</p>{/if}
				{#if note}<p class="note-msg">{note}</p>{/if}

				<div class="cfg-acts">
					{#if !hosting}<button class="btn" onclick={onCheck} disabled={busy || catalogLoading}>
							{busy ? 'проверяю…' : 'проверить'}
						</button>{/if}
					<button class="btn prim" onclick={onSave} disabled={busy || catalogLoading}>
						{busy ? 'сохраняю…' : 'сохранить'}
					</button>
				</div>
			</div>
		{/if}
	</div>
{/if}

<style>
	.catalog-actions {
		grid-column: 1 / -1;
	}
	.m {
		font-family: var(--mono);
		font-variant-numeric: tabular-nums;
	}

	.prov {
		background: var(--card-bg);
		border: 1px solid var(--border-subtle);
		border-radius: 8px;
		margin-bottom: 16px;
		overflow: hidden;
		transition: border-color 0.2s;
	}

	.prov-hosting {
		border-left: 3px solid #06b6d4;
	}

	.prov-registrar {
		border-left: 3px solid #f59e0b;
	}

	.prov-compact {
		margin-bottom: 8px;
	}

	.prov-head {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 12px 16px;
		border-bottom: 1px solid var(--border-subtle);
	}

	.compact-head {
		padding: 8px 14px;
		border-bottom: none;
	}

	.pname {
		font-size: 14px;
		font-weight: 600;
	}

	.badge {
		padding: 2px 7px;
		border-radius: 4px;
		font-size: 10px;
		font-weight: 600;
		letter-spacing: 0.02em;
	}

	.badge-hosting {
		color: #06b6d4;
		background: oklch(0.7 0.15 200 / 0.12);
	}

	.badge-registrar {
		color: #f59e0b;
		background: oklch(0.75 0.18 70 / 0.12);
	}

	.state {
		padding: 2px 7px;
		border-radius: 4px;
		font-size: 10px;
		font-weight: 500;
	}

	.st-bad {
		color: var(--bad);
		background: oklch(0.65 0.2 25 / 0.12);
	}

	.st-off {
		color: var(--text-3);
		background: oklch(0.52 0 0 / 0.12);
	}

	.right {
		margin-left: auto;
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.toggle-label {
		font-size: 11px;
		color: var(--text-3);
	}

	.toggle-btn {
		width: 36px;
		height: 20px;
		border-radius: 10px;
		background: var(--border);
		border: none;
		padding: 2px;
		cursor: pointer;
		position: relative;
		transition: background 0.2s;
	}

	.toggle-btn.on {
		background: var(--primary);
	}

	.toggle-knob {
		display: block;
		width: 16px;
		height: 16px;
		border-radius: 50%;
		background: #fff;
		transform: translateX(0);
		transition: transform 0.2s;
	}

	.toggle-btn.on .toggle-knob {
		transform: translateX(16px);
	}

	/* Facts strip */
	.pstats {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
		gap: 1px;
		background: var(--border-subtle);
	}

	.pcell {
		background: var(--card-bg);
		padding: 12px 16px;
		display: flex;
		flex-direction: column;
		justify-content: center;
	}

	.pcell .label {
		font-size: 11px;
		color: var(--text-3);
		margin-bottom: 3px;
	}

	.pcell .value {
		font-size: 18px;
		font-weight: 600;
		color: var(--text);
		display: flex;
		align-items: baseline;
		gap: 3px;
	}

	.pcell .sub {
		font-size: 11px;
		color: var(--text-3);
		margin-top: 3px;
	}

	.win-bad {
		color: var(--bad);
	}

	/* Chart on card */
	.chart-block {
		padding: 10px 16px 12px;
		background: var(--card-bg);
		border-top: 1px solid var(--border-subtle);
	}

	.chart-head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		margin-bottom: 6px;
	}

	.ch-label {
		font-size: 11px;
		color: var(--text-3);
	}

	.ch-val {
		font-size: 11px;
		color: var(--text-2);
	}

	.ch-val.bad {
		color: var(--bad);
		font-weight: 600;
	}

	.check-err {
		padding: 8px 16px;
		background: oklch(0.65 0.2 25 / 0.08);
		border-top: 1px solid var(--border-subtle);
		color: var(--bad);
		font-size: 11px;
	}

	/* Drawer toggle */
	.cfg-toggle {
		display: block;
		width: 100%;
		text-align: left;
		padding: 8px 16px;
		font-size: 11px;
		color: var(--text-2);
		background: var(--inset);
		border: none;
		border-top: 1px solid var(--border-subtle);
		cursor: pointer;
		transition:
			background 0.15s,
			color 0.15s;
	}

	.cfg-toggle:hover {
		color: var(--text);
		background: var(--surface);
	}

	.caret {
		display: inline-block;
		width: 12px;
		font-weight: 600;
	}

	/* Config drawer */
	.config {
		padding: 16px;
		background: var(--inset);
		border-top: 1px solid var(--border-subtle);
	}

	.fields {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: 12px;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 5px;
		font-size: 11px;
		color: var(--text-2);
	}

	.field input,
	.field select {
		padding: 6px 10px;
		font-size: 12px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--text);
	}

	.field input:focus,
	.field select:focus {
		outline: none;
		border-color: var(--primary);
	}

	.cfg-acts {
		display: flex;
		justify-content: flex-end;
		gap: 8px;
		margin-top: 14px;
	}

	.btn {
		padding: 5px 12px;
		border-radius: 6px;
		font-size: 12px;
		cursor: pointer;
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--text);
		transition: background 0.15s;
	}

	.btn:hover {
		background: var(--surface-hover);
	}

	.btn-sm {
		padding: 3px 8px;
		font-size: 11px;
	}

	.btn.prim {
		background: var(--primary);
		color: #fff;
		border-color: var(--primary);
		font-weight: 600;
	}

	.btn.prim:hover {
		opacity: 0.9;
	}

	.err-msg {
		color: var(--bad);
		font-size: 12px;
		margin-top: 8px;
	}

	.note-msg {
		color: var(--ok);
		font-size: 12px;
		margin-top: 8px;
	}
</style>
