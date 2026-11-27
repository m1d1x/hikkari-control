<script lang="ts">
	import { describeApiError } from '$lib/api/client';
	import {
		formatCents,
		setBoxLabel,
		setBoxPrice,
		setHosterLabel,
		type EntryFleet,
		type FleetBox,
		type FleetRelay,
		type ProviderStats,
		providerLabel,
	} from '$lib/api/entry';
	import {
		fetchProviderAccounts,
		saveProvider,
		saveProviderSecret,
		readProviderSecret,
		saveBoxBilling,
		type ProviderAccount,
		type SecretField,
	} from '$lib/api/machines';

	interface Props {
		fleet: EntryFleet;
		onRetireBox: (box: FleetBox, relays: FleetRelay[]) => void;
		onForgetBox: (box: FleetBox) => void;
		onMarkDeadRelay: (relay: FleetRelay) => void;
		/** opens the machine's card */
		onOpenBox: (box: FleetBox) => void;
		/** a machine's name or price changed here: the fleet is read again */
		onChanged?: () => void;
	}

	let { fleet, onRetireBox, onForgetBox, onMarkDeadRelay, onOpenBox, onChanged }: Props = $props();

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

	// ── The owner's own name for a machine: a double click on the name ──────
	// A single click still opens the card, a moment later, so that the first
	// click of a double one does not.

	const OPEN_DELAY_MS = 250;
	let openTimer: ReturnType<typeof setTimeout> | undefined;
	let renaming = $state<string | null>(null);
	let draft = $state('');
	let labelError = $state<string | null>(null);
	/** labels saved here before the next fleet answer carries them */
	let savedLabels = $state<Record<string, string>>({});

	function labelOf(box: FleetBox): string {
		return savedLabels[box.name] ?? box.label ?? '';
	}

	function clickName(box: FleetBox) {
		clearTimeout(openTimer);
		openTimer = setTimeout(() => onOpenBox(box), OPEN_DELAY_MS);
	}

	function startRename(box: FleetBox) {
		clearTimeout(openTimer);
		labelError = null;
		draft = labelOf(box);
		renaming = box.name;
	}

	async function finishRename(box: FleetBox, keep: boolean) {
		if (renaming !== box.name) return;
		renaming = null;
		const next = draft.trim();
		if (!keep || next === labelOf(box)) return;
		const before = savedLabels[box.name];
		savedLabels = { ...savedLabels, [box.name]: next };
		try {
			await setBoxLabel(box.name, next);
			onChanged?.();
		} catch (e) {
			const rest = { ...savedLabels };
			if (before === undefined) delete rest[box.name];
			else rest[box.name] = before;
			savedLabels = rest;
			labelError = `${box.name}: ${describeApiError(e)}`;
		}
	}

	function renameKey(e: KeyboardEvent, box: FleetBox) {
		if (e.key === 'Enter') finishRename(box, true);
		else if (e.key === 'Escape') finishRename(box, false);
	}

	// ── The price, the same way: a double click, "6 usd", "469 руб", "5,5 €" ──

	let pricing = $state<string | null>(null);
	let priceDraft = $state('');
	/** prices saved here before the next fleet answer carries them, in cents */
	let savedCents = $state<Record<string, number>>({});

	function centsOf(box: FleetBox): number | undefined {
		return savedCents[box.name] ?? box.monthlyCents ?? undefined;
	}

	function startPrice(box: FleetBox) {
		labelError = null;
		priceDraft = box.priceAmount != null ? `${box.priceAmount} ${box.priceCurrency ?? 'USD'}` : '';
		pricing = box.name;
	}

	/** "469 руб" → [469, "руб"]; no currency keeps the box's own, else USD */
	function parsePrice(text: string, box: FleetBox): [number, string] | null {
		const m = text.trim().match(/^([$€₽]?)\s*(\d+(?:[.,]\d+)?)\s*([^\d\s]*)$/u);
		if (!m) return null;
		const amount = Number(m[2].replace(',', '.'));
		const cur = m[3] || m[1] || box.priceCurrency || 'USD';
		return amount > 0 ? [amount, cur] : null;
	}

	async function finishPrice(box: FleetBox, keep: boolean) {
		if (pricing !== box.name) return;
		pricing = null;
		const text = priceDraft.trim();
		if (!keep || text === '') return;
		const parsed = parsePrice(text, box);
		if (!parsed) {
			labelError = `${box.name}: цена «${text}» не читается, пиши «6 usd», «469 руб» или «5,5 €»`;
			return;
		}
		if (parsed[0] === box.priceAmount && parsed[1].toUpperCase() === box.priceCurrency) return;
		try {
			const res = await setBoxPrice(box.name, parsed[0], parsed[1]);
			savedCents = { ...savedCents, [box.name]: res.monthlyCents };
			onChanged?.();
		} catch (e) {
			labelError = `${box.name}: ${describeApiError(e)}`;
		}
	}

	function priceKey(e: KeyboardEvent, box: FleetBox) {
		if (e.key === 'Enter') finishPrice(box, true);
		else if (e.key === 'Escape') finishPrice(box, false);
	}

	function focusSelect(node: HTMLInputElement) {
		node.focus();
		node.select();
	}

	// ── The providers table: one line per hoster with machines in service ───
	// What used to be two blocks (19.11, owner: «легко визуально следить за
	// флотом»): the economics of the provider comparison and the park's own
	// counts side by side, the machines one click below. A hoster with no
	// machine in service is not a line: its history stays in the comparison.

	const PROVIDER_COLORS: Record<string, string> = {
		aeza: '#ec4899',
		u1: '#10b981',
		vantage: '#06b6d4',
		vdsina: '#ff6600',
		hetzner: '#3b82f6',
		openstack: '#ed1944',
	};

	function providerColor(name: string, index: number): string {
		const known = PROVIDER_COLORS[name.toLowerCase()];
		if (known) return known;
		const palette = ['#f59e0b', '#8b5cf6', '#14b8a6', '#ec4899', '#06b6d4', '#10b981'];
		return palette[index % palette.length];
	}

	const totalLive = $derived(providerGroups.reduce((acc, g) => acc + g.boxes.length, 0));

	const sharePct = (n: number) => (totalLive === 0 ? 0 : Math.round((n / totalLive) * 100));

	/** Below this, rent over traffic is a figure about an idle machine, not about a terabyte. */
	const MIN_MEASURED_GB = 100;

	// ── Биллинг хостеров: где платим, сколько включено, когда следующее списание ──
	// Ручные покупки API не имеют вовсе, поэтому эти числа вводит владелец, а
	// хранит hub (internal/admin/machines_billing.go).
	let accounts = $state<Record<string, ProviderAccount>>({});
	let budgetDraft = $state<ProviderAccount | null>(null);
	let secretMenu = $state<{ x: number; y: number; kind: string; field: SecretField } | null>(null);
	let secretDraft = $state<{ login: string; password: string; apiKey: string }>({ login: '', password: '', apiKey: '' });
	let billingEdit = $state<string | null>(null);
	let billingDraft = $state('');

	async function loadAccounts() {
		try {
			const res = await fetchProviderAccounts();
			accounts = Object.fromEntries(res.accounts.map((a) => [a.provider, a]));
		} catch {
			// экран живёт без них: суммы и даты тогда пустые
		}
	}
	void loadAccounts();

	const daysLeft = (iso: string): number | null => {
		const t = Date.parse(iso + 'T00:00:00Z');
		if (Number.isNaN(t)) return null;
		return Math.round((t - Date.now()) / 86_400_000);
	};

	/** Ближайшая дата списания у хостера: самая ранняя из живых машин. */
	function nearestBilling(g: ProviderGroup): { iso: string; days: number | null } | null {
		const dates = g.boxes
			.filter((b) => !b.retiredAt && b.rentedUntil)
			.map((b) => b.rentedUntil!.slice(0, 10))
			.sort();
		if (dates.length === 0) return null;
		return { iso: dates[0], days: daysLeft(dates[0]) };
	}

	const ruDate = (iso: string) => iso.split('-').reverse().join('.');

	const ROLE_LABEL: Record<string, string> = { entry: 'вход', receiver: 'приёмник', egress: 'выход' };
	const roleLabel = (role?: string) => (role ? (ROLE_LABEL[role] ?? role) : '—');

	/** Что стоит в колонке трафика у машины: тариф её плана, если он вписан. */
	const boxQuotaText = (box: FleetBox) =>
		box.trafficQuotaGb == null ? null : `${Math.round(box.trafficQuotaGb).toLocaleString('ru-RU')} ГБ`;

	function blankAccount(kind: string): ProviderAccount {
		return {
			provider: kind, displayName: '', asn: '', billingUrl: '', apiHost: '', note: '',
			hasLogin: false, hasPassword: false, hasApiKey: false,
			machines: 0, monthlyCents: 0, currency: '',
		};
	}

	// Одно значение секрета: hub расшифровывает и пишет это в журнал админки.
	let billError = $state<string | null>(null);
	async function copySecret(kind: string, field: SecretField) {
		try {
			const value = await readProviderSecret(kind, field);
			await navigator.clipboard?.writeText(value);
			billError = null;
		} catch (e) {
			billError = describeApiError(e);
		}
	}

	async function saveAccountDraft(draft: ProviderAccount, secrets: { login?: string; password?: string; apiKey?: string }) {
		try {
			const saved = await saveProvider(draft.provider, {
				displayName: draft.displayName, asn: draft.asn, billingUrl: draft.billingUrl,
				apiHost: draft.apiHost, note: draft.note, spendCapCents: draft.spendCapCents ?? null,
			});
			const flags = Object.values(secrets).some((v) => v !== undefined)
				? await saveProviderSecret(draft.provider, secrets)
				: { hasLogin: saved.hasLogin, hasPassword: saved.hasPassword, hasApiKey: saved.hasApiKey };
			accounts = { ...accounts, [draft.provider]: { ...saved, ...flags } };
			budgetDraft = null;
			billError = null;
		} catch (e) {
			billError = describeApiError(e);
		}
	}

	// Цена и дата машины правятся двойным кликом, как и везде на этом экране.
	async function saveBilling(box: FleetBox, field: 'priceAmount' | 'rentedUntil' | 'trafficQuotaGb', raw: string) {
		const value = raw.trim();
		try {
			if (field === 'priceAmount') {
				const n = Number(value.replace(',', '.'));
				await saveBoxBilling(box.name, { priceAmount: Number.isFinite(n) ? n : 0, priceCurrency: 'EUR' });
			} else if (field === 'rentedUntil') {
				await saveBoxBilling(box.name, { rentedUntil: value || null });
			} else {
				await saveBoxBilling(box.name, { trafficQuotaGb: value ? Number(value) : null });
			}
			billError = null;
			onChanged?.();
		} catch (e) {
			billError = describeApiError(e);
		}
	}

	/** «2 100/32 000 ГБ»: слэш, без слов «из». */
	function trafficPair(used: number | null | undefined, quota: number | null | undefined): string | null {
		if (used == null && quota == null) return null;
		const one = (v: number) => Math.round(v).toLocaleString('ru-RU');
		const left = used == null ? '?' : one(used);
		return `${left}/${quota == null ? '∞' : one(quota)} ГБ`;
	}

	/** Сумма по хостеру: цена, вписанная владельцем, когда она есть у всех машин в одной валюте. */
	function groupMoney(g: ProviderGroup): string {
		const priced = g.boxes.filter((b) => b.priceAmount != null);
		const currencies = new Set(priced.map((b) => b.priceCurrency ?? ''));
		if (priced.length > 0 && priced.length === g.boxes.length && currencies.size === 1) {
			const sum = priced.reduce((acc, b) => acc + (b.priceAmount ?? 0), 0);
			const cur = [...currencies][0];
			const sign = cur === 'EUR' ? '€' : cur === 'RUB' ? '₽' : cur || '€';
			return `${sum.toLocaleString('ru-RU', { minimumFractionDigits: 2, maximumFractionDigits: 2 })} ${sign}`;
		}
		return formatCents(g.monthlyCents);
	}

	function formatTraffic(gb: number): string {
		if (gb >= 1000) return `${(gb / 1000).toFixed(1)} ТБ`;
		return `${gb < 10 ? gb.toFixed(2) : gb.toFixed(0)} ГБ`;
	}

	function formatLife(days: number): string {
		if (days < 1 / 24) return 'меньше часа';
		if (days < 2) return `${Math.round(days * 24)} ч`;
		return `${days.toFixed(days < 10 ? 1 : 0)} дн`;
	}

	// The owner's name for a hoster: a double click, like a machine's.
	let renamingProvider = $state<string | null>(null);
	let providerDraft = $state('');
	/** hoster labels saved here before the next fleet answer carries them */
	let savedProviderLabels = $state<Record<string, string>>({});

	function providerOwnLabel(g: ProviderGroup): string {
		return savedProviderLabels[g.provider] ?? g.stats?.label ?? '';
	}

	function startProviderRename(g: ProviderGroup) {
		labelError = null;
		providerDraft = providerOwnLabel(g);
		renamingProvider = g.provider;
	}

	async function finishProviderRename(g: ProviderGroup, keep: boolean) {
		if (renamingProvider !== g.provider) return;
		renamingProvider = null;
		const next = providerDraft.trim();
		if (!keep || next === providerOwnLabel(g)) return;
		const before = savedProviderLabels[g.provider];
		savedProviderLabels = { ...savedProviderLabels, [g.provider]: next };
		try {
			await setHosterLabel(g.provider, next);
			onChanged?.();
		} catch (e) {
			const rest = { ...savedProviderLabels };
			if (before === undefined) delete rest[g.provider];
			else rest[g.provider] = before;
			savedProviderLabels = rest;
			labelError = `${providerLabel(g.provider)}: ${describeApiError(e)}`;
		}
	}

	function providerRenameKey(e: KeyboardEvent, g: ProviderGroup) {
		if (e.key === 'Enter') finishProviderRename(g, true);
		else if (e.key === 'Escape') finishProviderRename(g, false);
	}

	/**
	 * Terabytes a month in one machine's plan, as the hub knows it. The screen
	 * used to write this per hoster (18.11); now the ceiling lives on the
	 * machine (entry_boxes.traffic_quota_gb) and this stays as the fallback
	 * for a hoster whose machines have no quota typed yet.
	 */
	function planOf(g: ProviderGroup): number | null {
		return g.stats?.planTb ?? null;
	}

	/**
	 * What a terabyte costs: one machine's rent over its plan's terabytes when
	 * the owner gave them, else the accrued rent over the measured traffic,
	 * and nothing while the traffic is too small to divide by: 0.02 GB made
	 * aeza's terabyte cost $195 500.
	 */
	function costPerTb(g: ProviderGroup): { text: string; byPlan: boolean } | null {
		const h = g.stats;
		if (!h) return null;
		const plan = planOf(g);
		if (plan !== null && h.perBoxMonthlyCents !== null) {
			return { text: formatCents(Math.round(h.perBoxMonthlyCents / plan)), byPlan: true };
		}
		if (h.trafficGb === null || h.trafficGb < MIN_MEASURED_GB || h.spentCents <= 0) return null;
		return { text: formatCents(Math.round(h.spentCents / (h.trafficGb / 1000))), byPlan: false };
	}

	function costHint(g: ProviderGroup): string {
		const gb = g.stats?.trafficGb;
		if (gb != null && gb > 0 && gb < MIN_MEASURED_GB) {
			return `за окно прошло ${formatTraffic(gb)}, меньше ${MIN_MEASURED_GB} ГБ: делить аренду не на что; впиши тб в тарифе`;
		}
		return 'нет трафика за окно; впиши тб в тарифе, чтобы считать по тарифу';
	}

	const filteredAllBoxes = $derived.by(() => {
		if (!searchQuery.trim()) return activeBoxes;
		const q = searchQuery.toLowerCase().trim();
		return activeBoxes.filter(
			(b) =>
				b.name.toLowerCase().includes(q) ||
				labelOf(b).toLowerCase().includes(q) ||
				b.provider.toLowerCase().includes(q) ||
				b.location.toLowerCase().includes(q) ||
				b.proto.toLowerCase().includes(q),
		);
	});
</script>

{#snippet boxPrice(box: FleetBox, suffix: boolean)}
	{#if pricing === box.name}
		<input
			class="rename-input price-input m"
			placeholder="6 usd"
			aria-label="цена машины {box.name} в месяц"
			bind:value={priceDraft}
			use:focusSelect
			onkeydown={(e) => priceKey(e, box)}
			onblur={() => finishPrice(box, true)}
		/>
	{:else}
		{@const cents = centsOf(box)}
		<button
			class="price-btn m"
			ondblclick={() => startPrice(box)}
			title={box.priceAmount != null
				? `${box.priceAmount} ${box.priceCurrency} в месяц у хостера; двойной клик: поменять`
				: 'двойной клик: вписать цену'}
		>
			{#if box.priceAmount != null}
				{box.priceAmount.toLocaleString('ru-RU', { maximumFractionDigits: 2 })}{box.priceCurrency === 'EUR'
					? ' €'
					: box.priceCurrency === 'RUB'
						? ' ₽'
						: ` ${box.priceCurrency ?? ''}`}
			{:else if cents == null}<i class="nodata">нет цены</i>{:else}{formatCents(cents)}{#if suffix}<small
					>/мес</small
				>{/if}{/if}
		</button>
	{/if}
{/snippet}

{#snippet boxName(box: FleetBox, cls: string)}
	{#if renaming === box.name}
		<input
			class="rename-input m"
			maxlength="40"
			placeholder={box.name}
			aria-label="своё имя машины {box.name}"
			bind:value={draft}
			use:focusSelect
			onkeydown={(e) => renameKey(e, box)}
			onblur={() => finishRename(box, true)}
		/>
	{:else}
		{@const label = labelOf(box)}
		<button
			class={cls}
			onclick={() => clickName(box)}
			ondblclick={() => startRename(box)}
			title="клик: карточка машины, двойной клик: своё имя"
		>
			{#if label}{label} <small class="real-name">{box.name}</small>{:else}{box.name}{/if}
		</button>
	{/if}
{/snippet}

<section class="card fleet-card">
	<div class="card-head">
		<div class="head-left">
			<h2>провайдеры и машины</h2>
			<span class="sub">{activeBoxes.length} в строю</span>
			{#if labelError}<span class="label-err" role="alert">{labelError}</span>{/if}
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

	<!-- ======== MODE 0: PROVIDERS → MACHINE → RELAY → DOMAIN / DEST ======== -->
	{#if mode === 'tree'}
		{#if providerGroups.length === 0}
			<div class="empty-state">нет активных машин в парке</div>
		{:else}
			<div class="alloc-wrap">
				<div class="alloc-bar" role="img" aria-label="машины в строю по провайдерам">
					{#each providerGroups as g, i (g.provider)}
						<div
							class="alloc-seg"
							style="width: {sharePct(g.boxes.length)}%; background-color: {providerColor(g.provider, i)}"
							title="{providerOwnLabel(g) || providerLabel(g.provider)}: {g.boxes.length} шт"
						></div>
					{/each}
				</div>
			</div>
			<div class="table-wrap">
				<table class="prov-table">
					<thead>
						<tr>
							<th>провайдер</th>
							<th class="num" style="width:56px" title="машин в строю">#</th>
							<th class="num" title="аренда машин в строю в месяц">в месяц</th>
							<th class="num" title="трафик за окно / включено в тарифы всех машин хостера">трафик</th>
							<th class="num" title="тариф одной машины / тб в тарифе">себес / тб</th>
							<th class="num" title="ближайшее списание у хостера">биллинг</th>
							<th style="width:104px"></th>
						</tr>
					</thead>
					<tbody>
						{#each providerGroups as g, i (g.provider)}
							{@const open = expandedProviders.has(g.provider)}
							{@const h = g.stats}
							{@const own = providerOwnLabel(g)}
							{@const plan = planOf(g)}
							{@const cost = costPerTb(g)}
							{@const nb = nearestBilling(g)}
							{@const quotaGb = (() => {
								const own = g.boxes.reduce((sum, b) => sum + (b.trafficQuotaGb ?? 0), 0);
								if (own > 0) return own;
								return plan !== null ? plan * 1000 * g.boxes.length : null;
							})()}
							{@const trafficText = trafficPair(h?.trafficGb ?? null, quotaGb)}
							<tr
								class="prov-row"
								class:open
								onclick={() => (expandedProviders = toggleIn(expandedProviders, g.provider))}
							>
								<td>
									<div class="prov-name-cell">
										<span class="caret">{open ? '▾' : '▸'}</span>
										<span class="dot" style="background-color: {providerColor(g.provider, i)}"></span>
										{#if renamingProvider === g.provider}
											<input
												class="rename-input m"
												maxlength="40"
												placeholder={providerLabel(g.provider)}
												aria-label="своё имя провайдера {providerLabel(g.provider)}"
												bind:value={providerDraft}
												use:focusSelect
												onclick={(e) => e.stopPropagation()}
												onkeydown={(e) => providerRenameKey(e, g)}
												onblur={() => finishProviderRename(g, true)}
											/>
										{:else}
											<button
												class="prov-name"
												onclick={(e) => {
													e.stopPropagation();
													const url = accounts[g.provider]?.billingUrl;
													if (url) window.open(url, '_blank', 'noopener');
												}}
												ondblclick={() => startProviderRename(g)}
												title="{accounts[g.provider]?.asn
													? accounts[g.provider].asn + ' · клик: биллинг · двойной клик: своё имя'
													: 'клик: биллинг · двойной клик: своё имя провайдера'}"
												>
												{#if own}{own}
													<small class="real-name">{providerLabel(g.provider)}</small>{:else}{providerLabel(
														g.provider,
													)}{/if}
											</button>
										{/if}
									</div>
								</td>
								<td class="num m" title="машин в строю">{g.boxes.length}</td>
								<td class="num m">
									{groupMoney(g)}
									{#if g.unpriced > 0}<small class="muted">+{g.unpriced}</small>{/if}
								</td>
								<td class="num m">
									{#if trafficText === null}
										<span class="muted" title="тариф не заполнен, трафика не мерили">-</span>
									{:else}
										<span title="трафик за окно / включено в тарифы всех машин">{trafficText}</span>
									{/if}
								</td>
								<td class="num m">
									{#if cost !== null}
										<span
											class="cost-val"
											class:measured={!cost.byPlan}
											title={cost.byPlan
												? 'тариф одной машины / тб в тарифе'
												: 'аренда за окно / трафик за окно; впиши тб в тарифе, чтобы считать по тарифу'}
											>{cost.text}</span
										>
									{:else}
										<span class="muted" title={costHint(g)}>-</span>
									{/if}
								</td>
								<td class="num m">
									{#if nb === null}
										<span class="muted" title="дата оплаты не задана">-</span>
									{:else}
										<span
											class="billing"
											class:soon={nb.days !== null && nb.days <= 3}
											class:warn={nb.days !== null && nb.days > 3 && nb.days <= 7}
											title="ближайшее списание у хостера"
											>{ruDate(nb.iso)}<small>{nb.days} дн</small></span
										>
									{/if}
								</td>
								<td class="prov-acts">
									<button
										class="cred-ic"
										class:set={accounts[g.provider]?.hasLogin}
										title="логин биллинга: клик — скопировать, пкм — задать"
										onclick={(e) => {
											e.stopPropagation();
											void copySecret(g.provider, 'login');
										}}
										oncontextmenu={(e) => {
											e.preventDefault();
											secretMenu = { x: e.clientX, y: e.clientY, kind: g.provider, field: 'login' };
										}}>☰</button
									>
									<button
										class="cred-ic"
										class:set={accounts[g.provider]?.hasPassword}
										title="пароль биллинга: клик — скопировать, пкм — задать"
										onclick={(e) => {
											e.stopPropagation();
											void copySecret(g.provider, 'password');
										}}
										oncontextmenu={(e) => {
											e.preventDefault();
											secretMenu = { x: e.clientX, y: e.clientY, kind: g.provider, field: 'password' };
										}}>⚿</button
									>
									<button
										class="cred-ic"
										title="API, ключ, лимит, AS"
										onclick={(e) => {
											e.stopPropagation();
											budgetDraft = { ...(accounts[g.provider] ?? blankAccount(g.provider)) };
											secretDraft = { login: '', password: '', apiKey: '' };
										}}>⚙</button
									>
								</td>
							</tr>
							{#if open}
								<tr class="prov-boxes">
									<td colspan="7">
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
												{@render boxName(box, 't-box-name m')}
												<span class="t-cell role" title="роль машины в каскаде">{roleLabel(box.role)}</span>
												<span class="t-cell m" title="цена машины в месяц, двойной клик — правка"
													>{@render boxPrice(box, true)}</span
												>
												<span class="t-cell m" title="включено в тариф; расход по машине считает хостер"
													>{#if boxQuotaText(box) === null}<span class="muted" title="тариф не вписан">-</span
														>{:else}{boxQuotaText(box)}{/if}</span
												>
												<span class="t-cell m billing-cell">
													{#if billingEdit === box.name}
														<input
															class="rename-input m"
															placeholder="2026-12-12"
															aria-label="дата оплаты машины {box.name}"
															bind:value={billingDraft}
															use:focusSelect
															onclick={(e) => e.stopPropagation()}
															onkeydown={(e) => {
																if (e.key === 'Enter') {
																	void saveBilling(box, 'rentedUntil', billingDraft);
																	billingEdit = null;
																} else if (e.key === 'Escape') billingEdit = null;
															}}
															onblur={() => (billingEdit = null)}
														/>
													{:else if box.rentedUntil}
														{@const left = daysLeft(box.rentedUntil.slice(0, 10))}
														<span
															class="billing"
															class:soon={left !== null && left <= 3}
															class:warn={left !== null && left > 3 && left <= 7}
															title="дата оплаты, двойной клик — правка"
															ondblclick={() => {
																billingEdit = box.name;
																billingDraft = box.rentedUntil!.slice(0, 10);
															}}
															>{ruDate(box.rentedUntil.slice(0, 10))}<small>{left} дн</small></span
															>
													{:else}
														<button
															class="price-btn m muted"
															onclick={(e) => e.stopPropagation()}
															ondblclick={() => {
																billingEdit = box.name;
																billingDraft = '';
															}}
															title="дата оплаты не задана: двойной клик — вписать">-</button
														>
													{/if}
												</span>
												<span class="t-cell status-dot-cell">
													{#if box.openIncident}
														<span class="status-bad" title={box.openIncident}><span class="bad-dot"></span></span>
													{:else if box.verdict === 'warn'}
														<span class="status-warn" title={box.verdictLabel || 'внимание'}
															><span class="warn-dot"></span></span
														>
													{:else}
														<span class="status-ok" title="чисто"><span class="ok-dot"></span></span>
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
																<span class="badge {protoBadgeClass(r.proto)}">{shortProto(r.proto)}</span><span
																	class="m">:{r.port}</span
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
									</td>
								</tr>
							{/if}
						{/each}
					</tbody>
				</table>
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
									{@render boxName(box, 'box-name m as-link')}
								</div>
							</td>
							<td>
								<span class="provider-pill">{providerLabel(box.provider)}</span>
							</td>
							<td>
								<span class="badge {protoBadgeClass(box.proto)}">{shortProto(box.proto)}</span>
							</td>
							<td class="num">
								<span class="price-val">{@render boxPrice(box, false)}</span>
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

{#if secretMenu}
	{@const acct = accounts[secretMenu.kind]}
	{@const filled = secretMenu.field === 'login' ? acct?.hasLogin : acct?.hasPassword}
	<button class="ctx-backdrop" aria-label="закрыть меню" onclick={() => (secretMenu = null)}></button>
	<div class="ctx-menu" style="left: {secretMenu.x}px; top: {secretMenu.y + window.scrollY}px">
		<button
			onclick={() => {
				const kind = secretMenu!.kind;
				budgetDraft = { ...(accounts[kind] ?? blankAccount(kind)) };
				secretDraft = { login: '', password: '', apiKey: '' };
				secretMenu = null;
			}}>{filled ? 'изменить' : 'задать'}</button
		>
		{#if filled}
			<button
				onclick={() => {
					void copySecret(secretMenu!.kind, secretMenu!.field);
					secretMenu = null;
				}}>скопировать</button
			>
			<button
				onclick={() => {
					const field = secretMenu!.field;
					void saveProviderSecret(secretMenu!.kind, { [field]: '' }).then((flags) => {
						accounts = { ...accounts, [secretMenu!.kind]: { ...accounts[secretMenu!.kind], ...flags } };
						secretMenu = null;
					});
				}}>удалить</button
			>
		{/if}
	</div>
{/if}

{#if budgetDraft}
	{@const draft = budgetDraft}
	<button class="ctx-backdrop" aria-label="закрыть настройки" onclick={() => (budgetDraft = null)}></button>
	<aside class="bill-drawer">
		<div class="bill-head">
			<span>{draft.displayName || providerLabel(draft.provider)}</span>
			<button class="kill" onclick={() => (budgetDraft = null)}>закрыть</button>
		</div>
		<label class="bill-field">адрес биллинга
			<input bind:value={draft.billingUrl} placeholder="https://my.aeza.net" />
		</label>
		<label class="bill-field">AS
			<input bind:value={draft.asn} placeholder="AS203273" />
		</label>
		<label class="bill-field">логин биллинга
			<input bind:value={secretDraft.login} placeholder={draft.hasLogin ? 'задан, пусто — не менять' : 'не задан'} />
		</label>
		<label class="bill-field">пароль биллинга
			<input type="password" bind:value={secretDraft.password} placeholder={draft.hasPassword ? 'задан, пусто — не менять' : 'не задан'} />
		</label>
		<label class="bill-field">API-хост
			<input bind:value={draft.apiHost} placeholder="—" />
		</label>
		<label class="bill-field">API-ключ
			<input type="password" bind:value={secretDraft.apiKey} placeholder={draft.hasApiKey ? 'задан, пусто — не менять' : 'не задан'} />
		</label>
		<label class="bill-field">лимит расходов в месяц, €
			<input
				type="number"
				min="0"
				value={draft.spendCapCents == null ? '' : (draft.spendCapCents / 100).toFixed(2)}
				oninput={(e) => (draft.spendCapCents = e.currentTarget.value === '' ? null : Math.round(Number(e.currentTarget.value) * 100))}
			/>
		</label>
		<label class="bill-field">заметка
			<input bind:value={draft.note} placeholder="—" />
		</label>
		<p class="bill-note">Секреты лежат на hub зашифрованными; открытие поля пишется в журнал админки.</p>
		{#if billError}<p class="bill-error">{billError}</p>{/if}
		<button class="bill-save" onclick={() => void saveAccountDraft(draft, Object.fromEntries(Object.entries(secretDraft).filter(([, v]) => v !== '')))}>
			сохранить
		</button>
	</aside>
{/if}

<style>
	/* ---------- providers: one line per hoster, its machines below ---------- */
	.alloc-wrap {
		padding: 12px 16px 10px;
	}

	.alloc-bar {
		display: flex;
		width: 100%;
		height: 8px;
		border-radius: 4px;
		overflow: hidden;
		background: var(--border-subtle, oklch(0.2 0 0));
	}

	.alloc-seg {
		height: 100%;
	}

	.prov-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 12px;
		text-align: left;
	}

	.prov-table th {
		padding: 8px 8px;
		color: var(--text-3, oklch(0.5 0 0));
		font-weight: 500;
		font-size: 11px;
		line-height: 1.25;
		border-bottom: 1px solid var(--border-subtle, oklch(0.2 0 0));
		vertical-align: bottom;
	}

	.prov-table th:first-child,
	.prov-table td:first-child {
		padding-left: 16px;
	}

	.prov-table td {
		padding: 9px 8px;
		border-bottom: 1px solid var(--border-subtle, oklch(0.18 0 0));
		vertical-align: middle;
		white-space: nowrap;
	}

	.prov-row {
		cursor: pointer;
	}

	.prov-row:hover,
	.prov-row.open {
		background: var(--inset, oklch(0.17 0 0));
	}

	.prov-table .prov-boxes > td {
		padding: 0;
		white-space: normal;
	}

	.prov-name-cell {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.prov-name-cell .caret {
		font-family: var(--mono, monospace);
		color: var(--text-3, oklch(0.42 0 0));
	}

	.prov-name {
		padding: 0;
		font: inherit;
		font-size: 13px;
		font-weight: 600;
		text-align: left;
		color: var(--text, oklch(0.93 0 0));
		background: none;
		border: none;
		cursor: text;
	}

	.prov-name:hover {
		color: var(--primary, oklch(0.75 0.14 350));
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.prov-table .num {
		text-align: right;
		font-variant-numeric: tabular-nums;
	}

	.share,
	.muted {
		color: var(--text-3, oklch(0.42 0 0));
	}

	.prov-table .num small {
		margin-left: 4px;
	}

	.plan-input {
		width: 7ch;
		text-align: right;
	}

	.cost-val {
		color: var(--ok, oklch(0.72 0.15 155));
		font-weight: 600;
	}

	.cost-val.measured {
		color: var(--text-2, oklch(0.62 0 0));
	}

	/* ---------- tree: provider → machine → relay → domain / dest ---------- */

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

	.label-err {
		font-size: 11px;
		color: var(--bad, oklch(0.7 0.18 25));
	}

	.price-btn {
		padding: 0;
		font: inherit;
		color: inherit;
		background: none;
		border: none;
		cursor: text;
	}

	.price-btn:hover {
		color: var(--primary, oklch(0.75 0.14 350));
	}

	.price-input {
		width: 11ch;
		text-align: right;
	}

	.real-name {
		margin-left: 4px;
		font-size: 10px;
		font-weight: 400;
		color: var(--text-3, oklch(0.42 0 0));
	}

	.rename-input {
		width: 16ch;
		padding: 1px 4px;
		font-size: 12px;
		font-weight: 600;
		color: var(--text, oklch(0.93 0 0));
		background: var(--surface-2, oklch(0.22 0 0));
		border: 1px solid var(--primary, oklch(0.75 0.14 350));
		border-radius: 4px;
		outline: none;
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
	/* ---------- биллинг хостера: дата оплаты, значки секретов, шторка ---------- */
	.billing {
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}

	.billing-cell {
		min-width: 128px;
	}

	.status-dot-cell {
		display: flex;
		align-items: center;
		gap: 4px;
	}

	.ok-dot {
		display: inline-block;
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--ok, oklch(0.72 0.15 155));
	}

	.billing small {
		margin-left: 5px;
		color: var(--text-3, oklch(0.42 0 0));
		font-family: var(--mono, monospace);
		font-size: 10px;
	}

	.billing.soon,
	.billing.soon small {
		color: var(--red, oklch(0.65 0.2 25));
	}

	.billing.warn,
	.billing.warn small {
		color: var(--warn, oklch(0.78 0.12 85));
	}

	.prov-acts {
		display: flex;
		gap: 2px;
		justify-content: flex-end;
		white-space: nowrap;
	}

	.cred-ic {
		width: 24px;
		height: 24px;
		display: grid;
		place-items: center;
		border-radius: 5px;
		color: var(--text-3, oklch(0.42 0 0));
		font-size: 12px;
		line-height: 1;
	}

	.cred-ic:hover {
		background: var(--surface-hover, oklch(0.26 0 0));
		color: var(--text, oklch(0.93 0 0));
	}

	.cred-ic.set {
		color: var(--text-2, oklch(0.62 0 0));
	}

	.t-box .role {
		color: var(--text-2, oklch(0.62 0 0));
	}

	.ctx-backdrop {
		position: fixed;
		inset: 0;
		z-index: 40;
		background: none;
	}

	.ctx-menu {
		position: absolute;
		z-index: 41;
		min-width: 168px;
		padding: 4px;
		background: var(--card-bg, oklch(0.27 0 0));
		border: 1px solid var(--border, oklch(0.24 0 0));
		border-radius: 8px;
		box-shadow: 0 10px 28px rgba(0, 0, 0, 0.55);
	}

	.ctx-menu button {
		display: block;
		width: 100%;
		padding: 7px 10px;
		border-radius: 5px;
		text-align: left;
		font-size: 12px;
	}

	.ctx-menu button:hover {
		background: var(--surface-hover, oklch(0.26 0 0));
	}

	.bill-drawer {
		position: fixed;
		top: 0;
		right: 0;
		bottom: 0;
		z-index: 41;
		width: 340px;
		padding: 18px 20px 24px;
		overflow-y: auto;
		background: var(--bg-raised, oklch(0.17 0 0));
		border-left: 1px solid var(--border, oklch(0.24 0 0));
	}

	.bill-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 16px;
		font-size: 14px;
		font-weight: 600;
	}

	.bill-field {
		display: block;
		margin-bottom: 11px;
		color: var(--text-3, oklch(0.42 0 0));
		font-size: 11px;
	}

	.bill-field input {
		width: 100%;
		margin-top: 4px;
		padding: 7px 9px;
		background: var(--bg, oklch(0.14 0 0));
		border: 1px solid var(--border, oklch(0.24 0 0));
		border-radius: 6px;
		color: var(--text, oklch(0.93 0 0));
		font-size: 12px;
	}

	.bill-note {
		margin: 12px 0 0;
		color: var(--text-3, oklch(0.42 0 0));
		font-size: 11px;
		line-height: 1.5;
	}

	.bill-error {
		margin: 8px 0 0;
		color: var(--red, oklch(0.65 0.2 25));
		font-size: 11px;
	}

	.bill-save {
		margin-top: 14px;
		padding: 8px 14px;
		border-radius: 6px;
		background: var(--primary, oklch(0.75 0.14 350));
		color: oklch(0.16 0 0);
		font-size: 12px;
		font-weight: 600;
	}

	.bill-save:hover {
		background: var(--primary-hover, oklch(0.8 0.14 350));
	}

</style>
