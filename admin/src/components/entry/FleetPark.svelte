<script lang="ts">
	import { formatCents, type EntryFleet, type FleetBox, type FleetRelay, type ProviderStats } from '$lib/api/entry';

	interface Props {
		fleet: EntryFleet;
		onRetireBox: (box: FleetBox, relays: FleetRelay[]) => void;
		onForgetBox: (box: FleetBox) => void;
		onMarkDeadRelay: (relay: FleetRelay) => void;
		/** opens the machine's card */
		onOpenBox: (box: FleetBox) => void;
	}

	let { fleet, onRetireBox, onForgetBox, onMarkDeadRelay, onOpenBox }: Props = $props();

	type FleetViewMode = 'tree' | 'locations' | 'eyes' | 'all';
	let mode = $state<FleetViewMode>('tree');

	// ── The tree: provider → machine → relay → domain or dest ──────────────

	let expandedProviders = $state<Set<string>>(new Set());
	let expandedTreeBoxes = $state<Set<string>>(new Set());

	const RELAY_STATUS: Record<string, string> = {
		alive: 'в пуле',
		pending: 'ждёт допуска',
		testing: 'канарейка',
		draining: 'сливается',
		dead: 'мёртв',
	};

	interface ProviderGroup {
		provider: string;
		boxes: FleetBox[];
		liveRelays: number;
		holders: number;
		monthlyCents: number;
		unpriced: number;
		incidents: number;
		stats: ProviderStats | undefined;
	}

	const providerGroups = $derived.by(() => {
		const map = new Map<string, ProviderGroup>();
		for (const box of activeBoxes) {
			let g = map.get(box.provider);
			if (!g) {
				g = {
					provider: box.provider,
					boxes: [],
					liveRelays: 0,
					holders: 0,
					monthlyCents: 0,
					unpriced: 0,
					incidents: 0,
					stats: fleet.providers?.hosting.find((p) => p.provider === box.provider),
				};
				map.set(box.provider, g);
			}
			g.boxes.push(box);
			g.liveRelays += box.relays.filter((r) => r.status !== 'dead').length;
			g.holders += boxGrants(box);
			if (box.monthlyCents == null) g.unpriced++;
			else g.monthlyCents += box.monthlyCents;
			if (box.openIncident || box.verdict === 'bad' || box.verdict === 'warn') g.incidents++;
		}
		return Array.from(map.values()).sort((a, b) => b.boxes.length - a.boxes.length);
	});

	function toggleIn(set: Set<string>, key: string): Set<string> {
		const next = new Set(set);
		if (next.has(key)) next.delete(key);
		else next.add(key);
		return next;
	}

	/** Where a relay leads: an xhttp box's domain, or the reality dest it borrows. */
	function relayLeaf(box: FleetBox, r: FleetRelay): { kind: string; value: string } | null {
		if (r.unreadable) return null;
		if (shortProto(r.proto || box.proto) === 'xhttp') {
			return box.domain ? { kind: 'домен', value: box.domain } : null;
		}
		return r.cover ? { kind: 'дест', value: r.cover } : null;
	}

	function boxRegion(box: FleetBox): string {
		return box.providerRegion || box.region || box.location;
	}

	let expandedLocations = $state<Set<string>>(new Set(['nl']));
	let locProtoFilter = $state<Record<string, string>>({});
	let expandedBoxRelays = $state<Set<string>>(new Set());
	let searchQuery = $state('');

	type SortField = 'name' | 'provider' | 'proto' | 'monthlyCents' | 'relays' | 'createdAt' | 'status';
	let sortField = $state<SortField>('name');
	let sortDir = $state<'asc' | 'desc'>('asc');

	function normalizeGeo(loc: string): { key: string; label: string; country: string } {
		if (!loc) return { key: 'unknown', label: 'неизвестно', country: '??' };
		const l = loc.toLowerCase().trim();
		if (l.includes('amsterdam') || l.endsWith(', nl') || l === 'nl') {
			return { key: 'nl', label: 'Нидерланды (AMS)', country: 'NL' };
		}
		if (l.includes('frankfurt') || l.endsWith(', de') || l === 'de') {
			return { key: 'de', label: 'Германия (FRA)', country: 'DE' };
		}
		if (l.includes('helsinki') || l.endsWith(', fi') || l === 'fi') {
			return { key: 'fi', label: 'Финляндия (HEL)', country: 'FI' };
		}
		if (l.includes('stockholm') || l.endsWith(', se') || l === 'se') {
			return { key: 'se', label: 'Швеция (STO)', country: 'SE' };
		}
		if (l.includes('london') || l.endsWith(', gb') || l.endsWith(', uk')) {
			return { key: 'gb', label: 'Великобритания (LON)', country: 'GB' };
		}
		if (l.includes('singapore') || l.endsWith(', sg')) {
			return { key: 'sg', label: 'Сингапур (SGP)', country: 'SG' };
		}
		if (l.includes('paris') || l.endsWith(', fr')) {
			return { key: 'fr', label: 'Франция (PAR)', country: 'FR' };
		}
		if (loc.includes(',')) {
			const parts = loc.split(',');
			const city = parts[0].trim();
			const cc = parts[1].trim().toUpperCase();
			return { key: cc.toLowerCase(), label: `${city} (${cc})`, country: cc };
		}
		return { key: l.slice(0, 3), label: loc, country: loc.toUpperCase().slice(0, 2) };
	}

	function shortProto(proto: string): string {
		if (!proto) return 'unknown';
		if (proto.includes('reality')) return 'reality';
		if (proto.includes('hyst')) return 'hysteria2';
		if (proto.includes('xhttp')) return 'xhttp';
		if (proto.includes('amnezia')) return 'amneziawg';
		return proto;
	}

	function protoColor(proto: string): string {
		const p = shortProto(proto);
		if (p === 'reality') return '#ec4899';
		if (p === 'xhttp') return '#f59e0b';
		if (p === 'hysteria2') return '#06b6d4';
		if (p === 'amneziawg') return '#8b5cf6';
		return '#a855f7';
	}

	function protoBadgeClass(proto: string): string {
		const p = shortProto(proto);
		if (p === 'reality') return 'proto-reality';
		if (p === 'hysteria2') return 'proto-hyst';
		if (p === 'xhttp') return 'proto-xhttp';
		if (p === 'amneziawg') return 'proto-amnezia';
		return '';
	}

	function boxLifetime(iso: string): string {
		if (!iso) return 'нет';
		const diff = Math.max(0, Date.now() - new Date(iso).getTime());
		const hours = diff / 3_600_000;
		if (hours < 1) return 'только что';
		if (hours < 24) return `${Math.floor(hours)} ч`;
		return `${Math.floor(hours / 24)} дн`;
	}

	function boxGrants(box: FleetBox): number {
		return box.relays.reduce((acc, r) => acc + (r.grants || 0), 0);
	}

	const activeBoxes = $derived.by(() => {
		if (!fleet?.boxes) return [];
		return fleet.boxes.filter((b) => !b.retiredAt);
	});

	const needEyesBoxes = $derived.by(() => {
		return activeBoxes.filter((b) => b.openIncident || b.verdict === 'bad' || b.verdict === 'warn');
	});

	interface LocationProtoStat {
		proto: string;
		label: string;
		count: number;
		pct: number;
		color: string;
	}

	interface LocationGroup {
		key: string;
		label: string;
		country: string;
		boxes: FleetBox[];
		totalBoxes: number;
		pctOfFleet: number;
		totalRelays: number;
		incidentCount: number;
		protoStats: LocationProtoStat[];
	}

	const locationGroups = $derived.by(() => {
		const map = new Map<string, LocationGroup>();

		for (const box of activeBoxes) {
			const geo = normalizeGeo(box.location);
			let g = map.get(geo.key);
			if (!g) {
				g = {
					key: geo.key,
					label: geo.label,
					country: geo.country,
					boxes: [],
					totalBoxes: 0,
					pctOfFleet: 0,
					totalRelays: 0,
					incidentCount: 0,
					protoStats: [],
				};
				map.set(geo.key, g);
			}

			g.boxes.push(box);
			g.totalBoxes++;
			const liveRelays = box.relays.filter((r) => r.status !== 'dead');
			g.totalRelays += liveRelays.length;

			if (box.openIncident || box.verdict === 'bad' || box.verdict === 'warn') {
				g.incidentCount++;
			}
		}

		const totalFleetBoxes = activeBoxes.length || 1;
		const list = Array.from(map.values());

		for (const g of list) {
			g.pctOfFleet = Math.round((g.totalBoxes / totalFleetBoxes) * 100);

			const pMap = new Map<string, number>();
			for (const b of g.boxes) {
				const p = shortProto(b.proto);
				pMap.set(p, (pMap.get(p) || 0) + 1);
			}

			g.protoStats = Array.from(pMap.entries())
				.map(([proto, count]) => ({
					proto,
					label: proto,
					count,
					pct: Math.round((count / g.totalBoxes) * 100),
					color: protoColor(proto),
				}))
				.sort((a, b) => b.count - a.count);
		}

		return list.sort((a, b) => b.totalBoxes - a.totalBoxes || a.label.localeCompare(b.label));
	});

	function toggleLocation(key: string) {
		const next = new Set(expandedLocations);
		if (next.has(key)) {
			next.delete(key);
		} else {
			next.add(key);
		}
		expandedLocations = next;
	}

	function openLocationWithProto(locKey: string, proto: string) {
		const next = new Set(expandedLocations);
		next.add(locKey);
		expandedLocations = next;

		if (locProtoFilter[locKey] === proto) {
			locProtoFilter[locKey] = '';
		} else {
			locProtoFilter[locKey] = proto;
		}
	}

	function toggleBoxRelays(boxName: string) {
		const next = new Set(expandedBoxRelays);
		if (next.has(boxName)) {
			next.delete(boxName);
		} else {
			next.add(boxName);
		}
		expandedBoxRelays = next;
	}

	function toggleSort(field: SortField) {
		if (sortField === field) {
			sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		} else {
			sortField = field;
			sortDir = 'asc';
		}
	}

	function sortIndicator(field: SortField): string {
		if (sortField !== field) return '';
		return sortDir === 'asc' ? ' ▲' : ' ▼';
	}

	function getSortedBoxes(boxes: FleetBox[]): FleetBox[] {
		return [...boxes].sort((a, b) => {
			let cmp = 0;
			switch (sortField) {
				case 'name':
					cmp = a.name.localeCompare(b.name, undefined, { numeric: true });
					break;
				case 'provider':
					cmp = (a.provider || '').localeCompare(b.provider || '');
					break;
				case 'proto':
					cmp = shortProto(a.proto).localeCompare(shortProto(b.proto));
					break;
				case 'monthlyCents':
					cmp = (a.monthlyCents || 0) - (b.monthlyCents || 0);
					break;
				case 'relays':
					cmp = a.relays.length - b.relays.length;
					break;
				case 'createdAt':
					cmp = new Date(a.createdAt || 0).getTime() - new Date(b.createdAt || 0).getTime();
					break;
				case 'status': {
					const aBad = a.openIncident ? 2 : a.verdict === 'warn' ? 1 : 0;
					const bBad = b.openIncident ? 2 : b.verdict === 'warn' ? 1 : 0;
					cmp = aBad - bBad;
					break;
				}
			}
			return sortDir === 'asc' ? cmp : -cmp;
		});
	}

	const filteredAllBoxes = $derived.by(() => {
		if (!searchQuery.trim()) return activeBoxes;
		const q = searchQuery.toLowerCase().trim();
		return activeBoxes.filter(
			(b) =>
				b.name.toLowerCase().includes(q) ||
				b.provider.toLowerCase().includes(q) ||
				b.location.toLowerCase().includes(q) ||
				b.proto.toLowerCase().includes(q),
		);
	});
</script>

<section class="card fleet-card">
	<div class="card-head">
		<div class="head-left">
			<h2>парк машин</h2>
			<span class="sub">{activeBoxes.length} в строю</span>
		</div>

		<div class="right filters">
			<button class="chip" class:on={mode === 'tree'} onclick={() => (mode = 'tree')}> провайдеры </button>
			<button class="chip" class:on={mode === 'locations'} onclick={() => (mode = 'locations')}> локации </button>
			<button class="chip" class:on={mode === 'eyes'} onclick={() => (mode = 'eyes')}>
				{#if needEyesBoxes.length > 0}
					<span class="bad-dot"></span>
				{/if}
				требуют внимания · {needEyesBoxes.length}
			</button>
			<button class="chip" class:on={mode === 'all'} onclick={() => (mode = 'all')}>
				все машины · {activeBoxes.length}
			</button>
		</div>
	</div>

	<!-- ======== MODE 0: TREE: PROVIDER → MACHINE → RELAY → DOMAIN / DEST ======== -->
	{#if mode === 'tree'}
		{#if providerGroups.length === 0}
			<div class="empty-state">нет активных машин в парке</div>
		{:else}
			<div class="tree">
				{#each providerGroups as g (g.provider)}
					{@const open = expandedProviders.has(g.provider)}
					<button
						class="t-provider"
						class:open
						onclick={() => (expandedProviders = toggleIn(expandedProviders, g.provider))}
					>
						<span class="caret">{open ? '▾' : '▸'}</span>
						<span class="t-name">{g.provider}</span>
						<span class="t-meta">машин <b>{g.boxes.length}</b></span>
						<span class="t-meta">релеев <b>{g.liveRelays}</b></span>
						<span class="t-meta">держателей <b>{g.holders.toLocaleString('ru-RU')}</b></span>
						<span class="t-meta">
							в месяц <b>{formatCents(g.monthlyCents)}</b>
							{#if g.unpriced > 0}<small>без цены {g.unpriced}</small>{/if}
						</span>
						<span class="t-meta">
							выживаемость
							{#if g.stats?.winratePct == null}<i class="nodata">нет данных</i>{:else}<b
									>{g.stats.winratePct.toFixed(0)}%</b
								>{/if}
						</span>
						<span class="t-meta">
							медиана жизни
							{#if g.stats?.medianLifeDays == null}<i class="nodata">нет данных</i>{:else}<b
									>{g.stats.medianLifeDays.toFixed(1)} дн</b
								>{/if}
						</span>
						{#if g.incidents > 0}
							<span class="status-bad"><span class="bad-dot"></span>{g.incidents} в инциденте</span>
						{/if}
					</button>

					{#if open}
						{#each getSortedBoxes(g.boxes) as box (box.name)}
							{@const boxOpen = expandedTreeBoxes.has(box.name)}
							<div class="t-box" class:has-incident={box.openIncident}>
								<button
									class="caret-btn"
									aria-label="релеи машины {box.name}"
									onclick={() => (expandedTreeBoxes = toggleIn(expandedTreeBoxes, box.name))}
								>
									{boxOpen ? '▾' : '▸'}
								</button>
								<button class="t-box-name m" onclick={() => onOpenBox(box)} title="карточка машины">{box.name}</button>
								<span class="ip-sealed" title="ip запечатан, открывается в карточке">ip в конверте</span>
								<span class="t-cell">{boxRegion(box)}</span>
								<span class="t-cell m">{boxLifetime(box.createdAt)}</span>
								<span class="t-cell m">
									{#if box.monthlyCents == null}<i class="nodata">нет цены</i>{:else}{formatCents(
											box.monthlyCents,
										)}<small>/мес</small>{/if}
								</span>
								<span class="t-cell">
									{#if box.openIncident}
										<span class="status-bad"><span class="bad-dot"></span>{box.openIncident}</span>
									{:else if box.verdict === 'warn'}
										<span class="status-warn"><span class="warn-dot"></span>{box.verdictLabel || 'внимание'}</span>
									{:else}
										<span class="status-ok">чисто</span>
									{/if}
								</span>
							</div>
							{#if boxOpen}
								{#each box.relays as r (r.name)}
									{@const leaf = relayLeaf(box, r)}
									<div class="t-relay">
										<span class="rname m"><i class="dot {r.verdict}"></i>{r.name}</span>
										<span>
											{#if r.unreadable}
												<i class="nodata" title="ключ сервера не открывает запись релея">не читается</i>
											{:else}
												<span class="badge {protoBadgeClass(r.proto)}">{shortProto(r.proto)}</span><span class="m"
													>:{r.port}</span
												>
											{/if}
										</span>
										<span>{RELAY_STATUS[r.status] ?? r.status}</span>
										<span class="m">держателей {r.grants.toLocaleString('ru-RU')}</span>
										<span class="t-leaf">
											{#if leaf}
												<span class="leaf-kind">{leaf.kind}</span><span class="m">{leaf.value}</span>
											{:else}
												<i class="nodata">нет данных</i>
											{/if}
										</span>
									</div>
								{:else}
									<div class="t-relay"><i class="nodata">на машине нет релеев</i></div>
								{/each}
							{/if}
						{/each}
					{/if}
				{/each}
			</div>
		{/if}
	{/if}

	<!-- ======== MODE 1: LOCATIONS TABLE WITH MICRO PROTOCOL BARS ======== -->
	{#if mode === 'locations'}
		{#if locationGroups.length > 0}
			<div class="table-wrap">
				<table class="loc-table">
					<thead>
						<tr>
							<th class="col-loc">локация</th>
							<th class="col-proto-bar">баланс протоколов</th>
							<th class="num">машины</th>
							<th class="num">доля</th>
							<th class="num">релеи</th>
							<th class="col-status">статус</th>
							<th class="col-act"></th>
						</tr>
					</thead>
					<tbody>
						{#each locationGroups as loc (loc.key)}
							{@const isExpanded = expandedLocations.has(loc.key)}
							{@const activeFilter = locProtoFilter[loc.key] || ''}
							{@const locBoxes = activeFilter
								? loc.boxes.filter((b) => shortProto(b.proto) === activeFilter)
								: loc.boxes}
							{@const sortedBoxes = getSortedBoxes(locBoxes)}

							<tr class="loc-row" class:row-expanded={isExpanded} onclick={() => toggleLocation(loc.key)}>
								<td class="col-loc">
									<div class="loc-title-cell">
										<span class="country-pill">{loc.country}</span>
										<span class="loc-name">{loc.label}</span>
									</div>
								</td>

								<td class="col-proto-bar">
									<div class="proto-cell-content">
										<!-- Segmented micro bar -->
										<div class="loc-proto-bar" role="progressbar" aria-label="распределение протоколов">
											{#each loc.protoStats as ps (ps.proto)}
												<div
													class="proto-seg"
													style="width: {ps.pct}%; background-color: {ps.color}"
													title="{ps.label}: {ps.count} шт ({ps.pct}%)"
													onclick={(e) => {
														e.stopPropagation();
														openLocationWithProto(loc.key, ps.proto);
													}}
													role="button"
													tabindex="0"
													onkeydown={(e) => e.key === 'Enter' && openLocationWithProto(loc.key, ps.proto)}
												></div>
											{/each}
										</div>

										<!-- Legend chips with counts and percentages -->
										<div class="proto-legend">
											{#each loc.protoStats as ps (ps.proto)}
												<button
													class="proto-chip-btn"
													class:active={activeFilter === ps.proto}
													onclick={(e) => {
														e.stopPropagation();
														openLocationWithProto(loc.key, ps.proto);
													}}
													title="фильтровать по {ps.label}"
												>
													<span class="p-dot" style="background-color: {ps.color}"></span>
													<span class="p-name">{ps.label}</span>
													<span class="p-num m">{ps.count}</span>
													<span class="p-pct">({ps.pct}%)</span>
												</button>
											{/each}
										</div>
									</div>
								</td>

								<td class="num m">{loc.totalBoxes}</td>
								<td class="num m">{loc.pctOfFleet}%</td>
								<td class="num m">{loc.totalRelays}</td>
								<td class="col-status">
									{#if loc.incidentCount > 0}
										<span class="status-bad">
											<span class="bad-dot"></span>
											<span>{loc.incidentCount} инцидент</span>
										</span>
									{:else}
										<span class="status-ok">чисто</span>
									{/if}
								</td>
								<td class="col-act">
									<button class="chevron-btn" aria-label="детали локации">
										{isExpanded ? '▴' : '▾'}
									</button>
								</td>
							</tr>

							<!-- Expanded Sortable Machine Data Table -->
							{#if isExpanded}
								<tr class="expand-row">
									<td colspan="7">
										<div class="expanded-loc-panel">
											<div class="loc-panel-bar">
												<div class="loc-panel-title">
													машины: <span class="loc-hl">{loc.label}</span>
													<span class="loc-count-badge">
														{locBoxes.length}
														{locBoxes.length === 1 ? 'машина' : locBoxes.length < 5 ? 'машины' : 'машин'}
													</span>
												</div>

												<div class="proto-filter-tabs">
													<button
														class="tab-btn"
														class:active={!activeFilter}
														onclick={() => (locProtoFilter[loc.key] = '')}
													>
														все ({loc.boxes.length})
													</button>
													{#each loc.protoStats as ps (ps.proto)}
														<button
															class="tab-btn"
															class:active={activeFilter === ps.proto}
															onclick={() => (locProtoFilter[loc.key] = ps.proto)}
														>
															<span class="p-dot" style="background-color: {ps.color}"></span>
															{ps.label} ({ps.count})
														</button>
													{/each}
												</div>
											</div>

											{@render machinesTable(sortedBoxes)}
										</div>
									</td>
								</tr>
							{/if}
						{/each}
					</tbody>
				</table>
			</div>
		{:else}
			<div class="empty-state">нет активных машин в парке</div>
		{/if}
	{/if}

	<!-- ======== MODE 2: NEED EYES (INCIDENTS) ======== -->
	{#if mode === 'eyes'}
		{#if needEyesBoxes.length === 0}
			<div class="empty-state ok-state">
				<span class="ok-icon">✓</span>
				все машины чистые, инцидентов нет
			</div>
		{:else}
			<div class="pad-16">
				{@render machinesTable(getSortedBoxes(needEyesBoxes))}
			</div>
		{/if}
	{/if}

	<!-- ======== MODE 3: ALL MACHINES (DETAILED SORTABLE TABLE) ======== -->
	{#if mode === 'all'}
		<div class="search-bar">
			<input
				type="text"
				placeholder="поиск по имени, провайдеру, локации, протоколу"
				bind:value={searchQuery}
				class="search-input"
			/>
			<span class="search-count">показано {filteredAllBoxes.length} из {activeBoxes.length}</span>
		</div>
		<div class="pad-16">
			{@render machinesTable(getSortedBoxes(filteredAllBoxes))}
		</div>
	{/if}
</section>

<!-- Reusable Sortable Machine Data Table -->
{#snippet machinesTable(boxes: FleetBox[])}
	{#if boxes.length === 0}
		<div class="empty-state">нет подходящих машин</div>
	{:else}
		<div class="table-wrap">
			<table class="machines-table">
				<thead>
					<tr>
						<th class="sortable col-host" onclick={() => toggleSort('name')}>
							хост{sortIndicator('name')}
						</th>
						<th class="sortable" onclick={() => toggleSort('provider')}>
							провайдер{sortIndicator('provider')}
						</th>
						<th class="sortable" onclick={() => toggleSort('proto')}>
							протокол{sortIndicator('proto')}
						</th>
						<th class="sortable num" onclick={() => toggleSort('monthlyCents')}>
							тариф{sortIndicator('monthlyCents')}
						</th>
						<th class="sortable num" onclick={() => toggleSort('relays')}>
							релеи / выдачи{sortIndicator('relays')}
						</th>
						<th class="sortable num" onclick={() => toggleSort('createdAt')}>
							возраст{sortIndicator('createdAt')}
						</th>
						<th class="sortable" onclick={() => toggleSort('status')}>
							статус{sortIndicator('status')}
						</th>
						<th class="col-acts">действия</th>
					</tr>
				</thead>
				<tbody>
					{#each boxes as box (box.name)}
						{@const relaysOpen = expandedBoxRelays.has(box.name)}
						{@const grants = boxGrants(box)}
						<tr class="m-row" class:has-incident={box.openIncident}>
							<td class="col-host">
								<div class="host-cell">
									<button class="box-name m as-link" onclick={() => onOpenBox(box)} title="карточка машины"
										>{box.name}</button
									>
								</div>
							</td>
							<td>
								<span class="provider-pill">{box.provider}</span>
							</td>
							<td>
								<span class="badge {protoBadgeClass(box.proto)}">{shortProto(box.proto)}</span>
							</td>
							<td class="num">
								{#if box.monthlyCents}
									<span class="price-val m">{formatCents(box.monthlyCents)}</span>
								{:else}
									<span class="muted">нет цены</span>
								{/if}
							</td>
							<td class="num">
								<span class="m font-bold">{box.relays.length}</span>
								{#if grants > 0}
									<span class="sub-grants m">({grants.toLocaleString('ru-RU')})</span>
								{/if}
							</td>
							<td class="num m sub-muted">
								{boxLifetime(box.createdAt)}
							</td>
							<td class="col-box-status">
								{#if box.openIncident}
									<span class="status-bad">
										<span class="bad-dot"></span>
										<span>{box.openIncident}</span>
									</span>
								{:else if box.verdict === 'warn'}
									<span class="status-warn">
										<span class="warn-dot"></span>
										<span>{box.verdictLabel || 'внимание'}</span>
									</span>
								{:else}
									<span class="status-ok">чисто</span>
								{/if}
							</td>
							<td class="col-acts">
								<div class="act-group">
									{#if box.openIncident?.includes('ip')}
										<button class="btn danger" onclick={() => onRetireBox(box, box.relays)}>заменить</button>
									{:else if box.openIncident?.includes('домен')}
										<a class="btn fix" href="/domains">домен</a>
									{/if}
									{#if !box.retiredAt}
										<button class="btn" onclick={() => onRetireBox(box, box.relays)}>сдать</button>
										<button class="btn" onclick={() => onForgetBox(box)}>забыть ключ</button>
									{/if}
									{#if box.relays.length > 0}
										<button class="btn btn-relays" class:open={relaysOpen} onclick={() => toggleBoxRelays(box.name)}>
											{relaysOpen ? 'скрыть' : `релеи (${box.relays.length})`}
										</button>
									{/if}
								</div>
							</td>
						</tr>

						<!-- Inline Relays Sub-row -->
						{#if relaysOpen && box.relays.length > 0}
							<tr class="relay-subrow">
								<td colspan="8">
									<div class="r-grid">
										<div class="r-head">
											<span>релей</span>
											<span>протокол</span>
											<span>прикрытие</span>
											<span class="num">выдач</span>
											<span class="num">жизнь</span>
											<span></span>
										</div>
										{#each box.relays as r (r.name)}
											<div class="r-row">
												<span class="rname">
													<i class="dot {r.verdict}"></i>{r.name}
													{#if r.kind === 'bootstrap'}
														<span class="tag-kind">bootstrap</span>
													{/if}
												</span>
												{#if r.unreadable}
													<span class="cover muted" title="ключ сервера не открывает запись релея">не читается</span>
													<span class="cover"></span>
												{:else}
													<span class="cover">
														<span class="badge {protoBadgeClass(r.proto)}">{shortProto(r.proto)}</span>
														<span class="m">:{r.port}</span>
													</span>
													<span class="cover">{r.cover || 'нет'}</span>
												{/if}
												<span class="grants m num">{r.grants ? r.grants.toLocaleString('ru-RU') : 'нет'}</span>
												<span class="ttl num">{boxLifetime(r.signedAt)}</span>
												<span class="act">
													{#if r.status !== 'dead'}
														<button class="kill" onclick={() => onMarkDeadRelay(r)}>мёртв</button>
													{/if}
												</span>
											</div>
										{/each}
									</div>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
{/snippet}

<style>
	/* ---------- tree: provider → machine → relay → domain / dest ---------- */
	.tree {
		font-size: 12px;
	}

	.t-provider {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 6px 18px;
		width: 100%;
		padding: 10px 16px;
		text-align: left;
		font-size: 12px;
		color: var(--text-2, oklch(0.62 0 0));
		background: var(--inset, oklch(0.17 0 0));
		border: none;
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
		cursor: pointer;
	}

	.t-provider:hover {
		background: var(--surface, oklch(0.23 0 0));
	}

	.t-provider .caret {
		font-family: var(--mono, monospace);
		color: var(--text-3, oklch(0.42 0 0));
	}

	.t-provider .t-name {
		min-width: 90px;
		font-weight: 600;
		color: var(--text, oklch(0.93 0 0));
	}

	.t-meta b {
		margin-left: 3px;
		font-family: var(--mono, monospace);
		font-variant-numeric: tabular-nums;
		color: var(--text, oklch(0.93 0 0));
	}

	.t-meta small {
		margin-left: 5px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.t-box {
		display: grid;
		grid-template-columns: 18px minmax(110px, 1fr) 96px minmax(90px, 1fr) 60px 90px minmax(140px, 1.6fr);
		align-items: center;
		gap: 12px;
		padding: 7px 16px 7px 30px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
	}

	.t-box.has-incident {
		background: oklch(0.65 0.2 25 / 0.05);
	}

	.caret-btn {
		padding: 0;
		font-family: var(--mono, monospace);
		color: var(--text-3, oklch(0.42 0 0));
		background: none;
		border: none;
		cursor: pointer;
	}

	.t-box-name,
	.as-link {
		padding: 0;
		font-size: 12px;
		font-weight: 600;
		text-align: left;
		color: var(--text, oklch(0.93 0 0));
		background: none;
		border: none;
		cursor: pointer;
	}

	.t-box-name:hover,
	.as-link:hover {
		color: var(--primary, oklch(0.75 0.14 350));
		text-decoration: underline;
	}

	.ip-sealed {
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.t-cell {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--text-2, oklch(0.62 0 0));
	}

	.t-cell small {
		color: var(--text-3, oklch(0.42 0 0));
	}

	.t-relay {
		display: grid;
		grid-template-columns: minmax(110px, 1fr) 130px 100px 120px minmax(160px, 2fr);
		align-items: center;
		gap: 12px;
		padding: 5px 16px 5px 62px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
		color: var(--text-2, oklch(0.62 0 0));
	}

	.t-leaf {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dot {
		display: inline-block;
		width: 6px;
		height: 6px;
		margin-right: 6px;
		border-radius: 50%;
		background: var(--unknown, oklch(0.52 0 0));
	}

	.dot.ok {
		background: var(--ok, oklch(0.72 0.15 155));
	}
	.dot.warn {
		background: var(--warn, oklch(0.78 0.12 85));
	}
	.dot.bad {
		background: var(--bad, oklch(0.65 0.2 25));
	}

	.leaf-kind {
		margin-right: 6px;
		font-size: 10px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.nodata {
		color: var(--text-3, oklch(0.42 0 0));
		font-style: italic;
	}

	@media (max-width: 900px) {
		.t-box {
			grid-template-columns: 18px 1fr 70px 90px;
		}

		.t-box .ip-sealed,
		.t-box .t-cell:nth-of-type(3),
		.t-box .t-cell:nth-of-type(4) {
			display: none;
		}

		.t-relay {
			grid-template-columns: 1fr 1fr;
			padding-left: 30px;
		}
	}

	.fleet-card {
		background: var(--card-bg, oklch(0.2 0 0));
		border: 1px solid var(--border-subtle, oklch(0.19 0 0));
		border-radius: 8px;
		margin-bottom: 16px;
		overflow: hidden;
	}

	.card-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 12px 16px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
	}

	.head-left {
		display: flex;
		align-items: baseline;
		gap: 10px;
	}

	.head-left h2 {
		margin: 0;
		font-size: 13px;
		font-weight: 600;
		color: var(--text, oklch(0.93 0 0));
	}

	.sub {
		font-size: 11px;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.filters {
		display: flex;
		gap: 6px;
		align-items: center;
	}

	.chip {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 4px 10px;
		border-radius: 6px;
		font-size: 11px;
		font-weight: 500;
		border: 1px solid var(--border-subtle, oklch(0.2 0 0));
		background: transparent;
		color: var(--text-2, oklch(0.62 0 0));
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.chip:hover {
		background: var(--surface, oklch(0.23 0 0));
		color: var(--text, oklch(0.93 0 0));
	}

	.chip.on {
		background: var(--surface-active, oklch(0.29 0 0));
		color: var(--text, oklch(0.95 0 0));
		border-color: var(--border, oklch(0.3 0 0));
	}

	.bad-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--bad, oklch(0.65 0.2 25));
		flex-shrink: 0;
	}

	.warn-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--warn, oklch(0.78 0.12 85));
		flex-shrink: 0;
	}

	/* Table Wrap */
	/* inline-size containment: the wrap takes its width from the container and
	   never from its table. Without it the machines table, nested inside a
	   location row's colspan cell, widens the outer table past the card and
	   pushes the action buttons off-screen; with it the inner table scrolls
	   inside its own panel if it truly does not fit. */
	.table-wrap {
		overflow-x: auto;
		width: 100%;
		contain: inline-size;
	}

	/* Top Locations Table */
	.loc-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 13px;
		text-align: left;
	}

	.loc-table th {
		padding: 9px 16px;
		color: var(--text-3, oklch(0.5 0 0));
		font-weight: 500;
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
		white-space: nowrap;
	}

	.loc-table td {
		padding: 12px 16px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.18 0 0));
		vertical-align: middle;
		white-space: nowrap;
	}

	.loc-row {
		cursor: pointer;
		transition: background 0.12s ease;
	}

	.loc-row:hover {
		background: rgba(255, 255, 255, 0.025);
	}

	.loc-row.row-expanded {
		background: rgba(255, 255, 255, 0.035);
	}

	.col-loc {
		font-weight: 500;
		min-width: 140px;
	}

	/* The location name may wrap: the table has to fit the card, and the
	   name is the one cell in the row that reads fine on two lines. */
	.loc-table td.col-loc {
		white-space: normal;
	}

	.loc-title-cell {
		display: flex;
		align-items: center;
		gap: 9px;
	}

	.country-pill {
		display: inline-block;
		padding: 2px 6px;
		border-radius: 4px;
		font-family: var(--mono, monospace);
		font-size: 10px;
		font-weight: 600;
		color: var(--text-2, oklch(0.75 0 0));
		background: rgba(255, 255, 255, 0.08);
	}

	.loc-name {
		font-weight: 600;
		color: var(--text, oklch(0.95 0 0));
		font-size: 13px;
	}

	/* Only a floor, not the bar's natural width: the legend under it wraps,
	   so the column gives way when the card is narrow. */
	.col-proto-bar {
		min-width: 160px;
	}

	.proto-cell-content {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.loc-proto-bar {
		display: flex;
		width: 100%;
		height: 7px;
		border-radius: 4px;
		overflow: hidden;
		background: rgba(255, 255, 255, 0.06);
	}

	.proto-seg {
		height: 100%;
		transition: opacity 0.15s ease;
		cursor: pointer;
	}

	.proto-seg:hover {
		opacity: 0.8;
	}

	.proto-legend {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.proto-chip-btn {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 2px 7px;
		border-radius: 4px;
		font-size: 11px;
		background: rgba(255, 255, 255, 0.04);
		border: 1px solid rgba(255, 255, 255, 0.06);
		color: var(--text-2, oklch(0.7 0 0));
		cursor: pointer;
		transition: all 0.12s ease;
	}

	.proto-chip-btn:hover {
		background: rgba(255, 255, 255, 0.09);
		color: var(--text, #fff);
	}

	.proto-chip-btn.active {
		background: rgba(255, 255, 255, 0.14);
		border-color: rgba(255, 255, 255, 0.25);
		color: var(--text, #fff);
		font-weight: 600;
	}

	.p-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.p-name {
		font-weight: 500;
	}

	.p-num {
		font-family: var(--mono, monospace);
		font-weight: 600;
		color: var(--text, #fff);
	}

	.p-pct {
		color: var(--text-3, oklch(0.5 0 0));
		font-size: 10px;
	}

	.num {
		text-align: right;
		font-family: var(--mono, monospace);
		font-variant-numeric: tabular-nums;
	}

	.m {
		font-family: var(--mono, monospace);
	}

	.font-bold {
		font-weight: 600;
	}

	.col-status {
		white-space: nowrap;
	}

	.status-ok {
		display: inline-block;
		padding: 2px 8px;
		border-radius: 4px;
		font-size: 11px;
		font-weight: 500;
		color: var(--ok, oklch(0.72 0.15 155));
		background: rgba(16, 185, 129, 0.1);
	}

	.status-warn {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 2px 8px;
		border-radius: 4px;
		font-size: 11px;
		font-weight: 500;
		color: var(--warn, oklch(0.78 0.12 85));
		background: rgba(245, 158, 11, 0.1);
	}

	.status-bad {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 2px 8px;
		border-radius: 4px;
		font-size: 11px;
		font-weight: 500;
		color: var(--bad, oklch(0.65 0.2 25));
		background: rgba(239, 68, 68, 0.1);
	}

	.col-act {
		width: 32px;
		text-align: center;
		padding-right: 12px;
	}

	.chevron-btn {
		background: transparent;
		border: none;
		color: var(--text-3, oklch(0.5 0 0));
		font-size: 14px;
		cursor: pointer;
		padding: 4px;
	}

	/* Expanded Panel Under Location */
	.expand-row td {
		padding: 0;
		background: rgba(0, 0, 0, 0.22);
		border-bottom: 1px solid var(--border-subtle, oklch(0.2 0 0));
	}

	.expanded-loc-panel {
		padding: 12px 16px 16px;
	}

	.loc-panel-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 10px;
		gap: 12px;
		flex-wrap: wrap;
	}

	.loc-panel-title {
		font-size: 12px;
		font-weight: 500;
		color: var(--text-2, oklch(0.7 0 0));
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.loc-hl {
		font-weight: 600;
		color: var(--text, #fff);
	}

	.loc-count-badge {
		font-size: 11px;
		color: var(--text-3, oklch(0.5 0 0));
	}

	.proto-filter-tabs {
		display: flex;
		gap: 5px;
		align-items: center;
	}

	.tab-btn {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		padding: 3px 8px;
		border-radius: 5px;
		font-size: 11px;
		font-weight: 500;
		border: 1px solid var(--border-subtle, oklch(0.24 0 0));
		background: rgba(255, 255, 255, 0.04);
		color: var(--text-2, oklch(0.68 0 0));
		cursor: pointer;
		transition: all 0.12s ease;
	}

	.tab-btn:hover {
		background: rgba(255, 255, 255, 0.08);
		color: var(--text, #fff);
	}

	.tab-btn.active {
		background: var(--surface-active, oklch(0.29 0 0));
		border-color: var(--border, oklch(0.35 0 0));
		color: var(--text, #fff);
	}

	/* Sortable Machines Table */
	.machines-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 12px;
		background: var(--surface, oklch(0.21 0 0));
		border: 1px solid var(--border-subtle, oklch(0.2 0 0));
		border-radius: 6px;
		overflow: hidden;
	}

	.machines-table th {
		padding: 8px 12px;
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-3, oklch(0.48 0 0));
		font-weight: 500;
		border-bottom: 1px solid var(--border-subtle, oklch(0.2 0 0));
		white-space: nowrap;
	}

	.machines-table th.sortable {
		cursor: pointer;
		user-select: none;
	}

	.machines-table th.sortable:hover {
		color: var(--text, #fff);
	}

	.machines-table td {
		padding: 8px 12px;
		border-bottom: 1px solid rgba(255, 255, 255, 0.04);
		vertical-align: middle;
		white-space: nowrap;
	}

	.m-row:hover {
		background: rgba(255, 255, 255, 0.03);
	}

	.m-row.has-incident {
		background: rgba(239, 68, 68, 0.04);
	}

	.col-host {
		min-width: 140px;
	}

	.host-cell {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.box-name {
		font-size: 12px;
		font-weight: 600;
		color: var(--text, oklch(0.95 0 0));
	}

	.box-ip {
		font-size: 10px;
		color: var(--text-3, oklch(0.45 0 0));
	}

	.provider-pill {
		display: inline-block;
		padding: 1px 6px;
		border-radius: 4px;
		font-size: 11px;
		background: rgba(255, 255, 255, 0.05);
		color: var(--text-2, oklch(0.7 0 0));
	}

	.badge {
		padding: 2px 7px;
		border-radius: 4px;
		font-size: 11px;
		background: rgba(255, 255, 255, 0.06);
		color: var(--text-2, oklch(0.7 0 0));
	}

	.badge.proto-reality {
		color: #f472b6;
		background: rgba(236, 72, 153, 0.12);
	}

	.badge.proto-hyst {
		color: #22d3ee;
		background: rgba(6, 182, 212, 0.12);
	}

	.badge.proto-xhttp {
		color: #fbbf24;
		background: rgba(245, 158, 11, 0.12);
	}

	.badge.proto-amnezia {
		color: #a78bfa;
		background: rgba(139, 92, 246, 0.12);
	}

	.price-val {
		color: var(--ok, oklch(0.72 0.15 155));
	}

	.sub-grants {
		font-size: 10px;
		color: var(--text-3, oklch(0.45 0 0));
		margin-left: 4px;
	}

	.sub-muted {
		color: var(--text-3, oklch(0.48 0 0));
		font-size: 11px;
	}

	.muted {
		color: var(--text-3, oklch(0.45 0 0));
	}

	/* An incident reads as a sentence ("ip машины забанен целиком: …"), so it
	   wraps instead of setting the width of the whole table. */
	.machines-table td.col-box-status {
		white-space: normal;
		min-width: 140px;
		max-width: 260px;
	}

	.col-box-status .status-bad,
	.col-box-status .status-warn {
		align-items: flex-start;
		line-height: 1.35;
	}

	.col-box-status .bad-dot,
	.col-box-status .warn-dot {
		margin-top: 4px;
	}

	.col-acts {
		text-align: right;
	}

	/* Buttons wrap rather than set the table's width: a row with an incident
	   carries four of them, and on one line they alone outgrow the card. */
	.machines-table td.col-acts {
		white-space: normal;
	}

	.act-group {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: flex-end;
		gap: 5px;
	}

	.btn {
		background: rgba(255, 255, 255, 0.06);
		border: 1px solid var(--border-subtle, oklch(0.24 0 0));
		color: var(--text, oklch(0.9 0 0));
		padding: 2px 7px;
		border-radius: 4px;
		font-size: 11px;
		cursor: pointer;
		text-decoration: none;
		transition: all 0.12s ease;
		white-space: nowrap;
	}

	.btn:hover {
		background: rgba(255, 255, 255, 0.12);
	}

	.btn.danger {
		background: rgba(239, 68, 68, 0.15);
		border-color: rgba(239, 68, 68, 0.35);
		color: #f87171;
	}

	.btn.danger:hover {
		background: rgba(239, 68, 68, 0.25);
	}

	.btn.fix {
		background: rgba(16, 185, 129, 0.15);
		border-color: rgba(16, 185, 129, 0.35);
		color: #34d399;
	}

	.btn-relays {
		color: var(--text-2, oklch(0.7 0 0));
	}

	.btn-relays.open {
		background: rgba(255, 255, 255, 0.14);
		color: #fff;
	}

	/* Relay subrow */
	.relay-subrow td {
		padding: 0;
		background: rgba(0, 0, 0, 0.3);
		border-bottom: 1px solid var(--border-subtle, oklch(0.2 0 0));
	}

	.r-grid {
		padding: 6px 14px 10px;
	}

	.r-head {
		display: grid;
		grid-template-columns: 2fr 1.2fr 2fr 1fr 1fr 60px;
		font-size: 10px;
		color: var(--text-3, oklch(0.45 0 0));
		text-transform: uppercase;
		letter-spacing: 0.04em;
		padding: 4px 0;
		border-bottom: 1px solid rgba(255, 255, 255, 0.04);
	}

	.r-row {
		display: grid;
		grid-template-columns: 2fr 1.2fr 2fr 1fr 1fr 60px;
		align-items: center;
		font-size: 11px;
		padding: 4px 0;
		border-top: 1px solid rgba(255, 255, 255, 0.02);
	}

	.rname {
		display: flex;
		align-items: center;
		gap: 6px;
		font-family: var(--mono, monospace);
		color: var(--text, oklch(0.9 0 0));
	}

	.dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.dot.ok {
		background: var(--ok, oklch(0.72 0.15 155));
	}
	.dot.warn {
		background: var(--warn, oklch(0.78 0.12 85));
	}
	.dot.bad {
		background: var(--bad, oklch(0.65 0.2 25));
	}

	.cover {
		color: var(--text-2, oklch(0.65 0 0));
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.ttl {
		color: var(--text-3, oklch(0.5 0 0));
	}

	.tag-kind {
		font-size: 9px;
		background: rgba(255, 255, 255, 0.08);
		padding: 1px 4px;
		border-radius: 3px;
	}

	.kill {
		background: transparent;
		border: 1px solid rgba(239, 68, 68, 0.3);
		color: #f87171;
		font-size: 10px;
		padding: 1px 6px;
		border-radius: 3px;
		cursor: pointer;
	}

	.kill:hover {
		background: rgba(239, 68, 68, 0.15);
	}

	/* Search bar */
	.search-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 10px 16px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.19 0 0));
		gap: 12px;
	}

	.search-input {
		background: rgba(0, 0, 0, 0.25);
		border: 1px solid var(--border-subtle, oklch(0.24 0 0));
		color: var(--text, #fff);
		border-radius: 6px;
		padding: 5px 12px;
		font-size: 12px;
		width: 340px;
		max-width: 100%;
	}

	.search-input:focus {
		outline: none;
		border-color: var(--primary, oklch(0.75 0.14 350));
	}

	.search-count {
		font-size: 11px;
		color: var(--text-3, oklch(0.45 0 0));
	}

	.pad-16 {
		padding: 14px 16px;
	}

	.empty-state {
		padding: 28px 16px;
		text-align: center;
		color: var(--text-3, oklch(0.5 0 0));
		font-size: 13px;
	}

	.empty-state.ok-state {
		color: var(--ok, oklch(0.72 0.15 155));
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 8px;
	}

	.ok-icon {
		font-weight: 700;
	}
</style>
