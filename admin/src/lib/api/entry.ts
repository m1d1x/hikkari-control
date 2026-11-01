import { ApiError, del, describeApiError, get, post, put } from './client';

// The entry pool (roadmap 42 П6.2, П6.15, internal/entry/admin.go). Owner-only
// on the server. Relays are born, deployed and signed on the hub; addresses and
// identities stay sealed at rest and are opened when the owner looks.
// Credentials never leave the seal.

export type RelayKind = 'personal' | 'bootstrap';

export type RelayStatus = 'pending' | 'alive' | 'testing' | 'draining' | 'suspect' | 'dead';

export interface PoolRelay {
	name: string;
	kind: RelayKind;
	/**
	 * pending: born on the hub, not admitted (no record, never handed out).
	 * testing: on the canary box while a new relay build is tried (П6.14).
	 * draining: its box is next in a rollout, holders are moved off first.
	 * Neither testing nor draining is handed out; both go back to alive.
	 * suspect: its machine was evicted on the eviction threshold and waits for
	 * its second chance (П9.11): back to alive, or dead with the machine.
	 */
	status: RelayStatus;
	keyId?: string;
	issuedAt?: string;
	expiresAt?: string;
	signedAt?: string;
	createdAt: string;
	diedAt?: string;
	/** Why it died (a dead relay only): the abuse dataset's label. */
	diedReason?: DeathReason;
	activeGrants: number;
	address: string;
	port: number;
	proto: string;
	/** The machine this relay runs on. */
	box: string;
	/** The server key could not open the identity; address/port/proto unknown. */
	unreadable?: boolean;
}

/** One rented machine (one address, one unit of failure). */
export interface PoolBox {
	name: string;
	address: string;
	provider: string;
	location: string;
	rentedUntil?: string;
	createdAt: string;
	/** The relay build the hub last saw or put on the box; absent if it never looked. */
	arch?: string;
	edgeSha?: string;
	edgeSeenAt?: string;
	/** Handed back to the provider (П6.10): every relay on it is dead. */
	retiredAt?: string;
	/** What the box speaks; the voice region of a hysteria2 box, the domain of a vless-xhttp one. */
	proto: string;
	region?: string;
	domain?: string;
	relays: string[];
	unreadable?: boolean;
	/**
	 * The rental as the hoster sold it (П9.2). Absent on a machine rented by
	 * hand: the hub can neither reboot nor hand that one back, and does not
	 * know its price.
	 */
	instanceId?: string;
	providerRegion?: string;
	plan?: string;
	monthlyCents?: number;
}

/** One nekko-edge build in the hub's image: the version a box should run. */
export interface EdgeBuild {
	arch: string;
	sha: string;
	size: number;
}

export function fetchPool(): Promise<{ relays: PoolRelay[]; boxes: PoolBox[]; edgeBuilds: EdgeBuild[] }> {
	return get('/api/v1/admin/entry/relays');
}

/**
 * Where a box stands against the hub's build for its architecture: current,
 * behind (a rollout will move it), or unknown (the hub never read its binary).
 */
export function buildState(box: PoolBox, builds: EdgeBuild[]): 'current' | 'behind' | 'unknown' {
	if (!box.edgeSha || !box.arch) return 'unknown';
	const want = builds.find((b) => b.arch === box.arch);
	if (!want) return 'unknown';
	return want.sha === box.edgeSha ? 'current' : 'behind';
}

export interface BoxGroup {
	/** null: relays whose address matches no imported box. */
	box: PoolBox | null;
	relays: PoolRelay[];
}

/**
 * The pool grouped by machine: boxes in name order, each with its relays,
 * then the relays no box claims. A box with no relay in the pool is still
 * listed — a rented machine with nothing on it is something to act on.
 */
export function groupByBox(relays: PoolRelay[], boxes: PoolBox[]): BoxGroup[] {
	const groups: BoxGroup[] = boxes.map((box) => ({ box, relays: relays.filter((r) => r.box === box.name) }));
	const loose = relays.filter((r) => !r.box);
	if (loose.length > 0) groups.push({ box: null, relays: loose });
	return groups;
}

/**
 * Protocol tag and its tone. One protocol per address (owner, 15.10), so the
 * tag also says which kind of block took a box down.
 */
export const PROTO: Record<string, { tag: string; tone: string }> = {
	'vless-reality-vision': { tag: 'reality', tone: 'reality' },
	hysteria2: { tag: 'hysteria', tone: 'hysteria' },
	'vless-xhttp': { tag: 'xhttp', tone: 'xhttp' },
	amneziawg: { tag: 'amnezia', tone: 'amnezia' },
};

// Provisioning (roadmap 42 П6.15): the hub takes a rented machine from the
// provider's password to the pool by itself, one job per run.

export type JobStatus = 'running' | 'done' | 'failed';

export interface Job {
	id: string;
	box: string;
	/**
	 * provision: the console's run; canary/update: a rollout's (П6.14); retire: the wipe of a box
	 * handed back (П6.10); restart/redest: the two repairs of П9.6б.
	 */
	kind: 'provision' | 'canary' | 'update' | 'retire' | 'restart' | 'redest' | 'prober';
	status: JobStatus;
	/** The step running now, or the one that failed: login, relays, harden, deploy <relay>, probe, sign. */
	step: string;
	error?: string;
	createdAt: string;
	finishedAt?: string;
}

export interface JobLine {
	seq: number;
	at: string;
	line: string;
}

/** The steps of a job in order, by the id the server reports (deploy carries the relay). */
export const JOB_STEPS: { id: string; label: string }[] = [
	{ id: 'login', label: 'вход' },
	{ id: 'relays', label: 'релеи' },
	{ id: 'harden', label: 'закрытие машины' },
	{ id: 'deploy', label: 'разворот' },
	{ id: 'probe', label: 'проба допуска' },
	{ id: 'sign', label: 'подпись' },
];

/** The steps of the wipe of a box handed back. */
export const RETIRE_STEPS: { id: string; label: string }[] = [
	{ id: 'login', label: 'вход' },
	{ id: 'wipe', label: 'стирание' },
];

/** The steps of the repair that only restarts stopped relay units (П9.6б). */
export const RESTART_STEPS: { id: string; label: string }[] = [
	{ id: 'login', label: 'вход' },
	{ id: 'restart', label: 'перезапуск юнитов' },
	{ id: 'probe', label: 'проба допуска' },
];

/** The steps of the repair that moves relays off a dead cover site (П9.6б). */
export const REDEST_STEPS: { id: string; label: string }[] = [
	{ id: 'login', label: 'вход' },
	{ id: 'dests', label: 'опрос прикрытий' },
	{ id: 'deploy', label: 'разворот' },
	{ id: 'probe', label: 'проба допуска' },
	{ id: 'sign', label: 'подпись' },
];

/** The steps of rolling out nekko-entry-prober onto an observation slot's box (П9.12). */
export const PROBER_STEPS: { id: string; label: string }[] = [
	{ id: 'login', label: 'вход' },
	{ id: 'deploy', label: 'разворот пробника' },
];

/** What a job is called in its panel, and the steps it goes through. */
export function jobShape(kind: Job['kind'] | undefined): { title: string; steps: { id: string; label: string }[] } {
	if (kind === 'retire') return { title: 'стирание', steps: RETIRE_STEPS };
	if (kind === 'restart') return { title: 'перезапуск релея', steps: RESTART_STEPS };
	if (kind === 'redest') return { title: 'смена прикрытия', steps: REDEST_STEPS };
	if (kind === 'prober') return { title: 'пробник', steps: PROBER_STEPS };
	return { title: 'разворот', steps: JOB_STEPS };
}

export interface NewBoxRequest {
	name: string;
	address: string;
	user: string;
	/** The provider's root password: used once by the job, never stored. Empty when the hub key is on the box. */
	password: string;
	provider: string;
	location: string;
	/** YYYY-MM-DD, optional. */
	rentedUntil: string;
	/** What every relay on the box speaks; one protocol per address. */
	proto: BoxProto;
	/** The voice region a hysteria2 box forwards to; empty otherwise. */
	region: string;
	/** A vless-xhttp box's own domain, its A record on the address; empty otherwise. */
	domain: string;
}

export type BoxProto = 'vless-reality-vision' | 'hysteria2' | 'vless-xhttp';

/** The protocols a new box can speak, in the order the console offers them. */
export const BOX_PROTOS: { id: BoxProto; label: string; hint: string }[] = [
	{ id: 'vless-reality-vision', label: 'reality', hint: 'поток под чужим прикрытием, основной вход' },
	{ id: 'vless-xhttp', label: 'xhttp', hint: 'поток как обычный сайт на своём домене' },
	{ id: 'hysteria2', label: 'hysteria', hint: 'голос: датаграммы до sfu одного региона' },
];

export function createBox(req: NewBoxRequest): Promise<{ jobId: string }> {
	return post('/api/v1/admin/entry/boxes', req);
}

export function provisionBox(name: string, password = ''): Promise<{ jobId: string }> {
	return post(`/api/v1/admin/entry/boxes/${encodeURIComponent(name)}/provision`, password ? { password } : undefined);
}

export function forgetHostKey(name: string): Promise<unknown> {
	return post(`/api/v1/admin/entry/boxes/${encodeURIComponent(name)}/forget-host-key`);
}

export function fetchJobs(): Promise<{ jobs: Job[] }> {
	return get('/api/v1/admin/entry/jobs');
}

export function fetchJob(id: string, after: number): Promise<{ job: Job; lines: JobLine[] }> {
	return get(`/api/v1/admin/entry/jobs/${encodeURIComponent(id)}`, { after });
}

export function fetchHubKey(): Promise<{ publicKey: string }> {
	return get('/api/v1/admin/entry/hub-key');
}

// The registrar ledger (roadmap 42 П4в Ф6): the pool of domains the hub owns,
// every purchase it made with its cost and order id, and names burned when a
// domain was blocked. The hub buys a relay's throwaway domain by itself; this
// is where each purchase shows.

/**
 * `burned` is a name under suspicion (out of the pool, reprobed apart from the
 * rounds); `dead` is one whose second chance failed — never drawn, not to be
 * renewed (roadmap 42 П9.11).
 */
export type DomainStatus = 'buying' | 'owned' | 'assigned' | 'burned' | 'dead' | 'failed';

export interface DomainLedgerRow {
	domain: string;
	tld: string;
	status: DomainStatus;
	box?: string;
	source: 'purchased' | 'seeded';
	costCents: number;
	orderId?: string;
	error?: string;
	boughtAt?: string;
	assignedAt?: string;
	burnedAt?: string;
	createdAt: string;
}

/** The registrars' spend against their group's caps (internal/entry/spend.go, П9.19). */
export interface DomainSpend extends SpendReading {
	/** The registrar kind domains are bought through; empty = pool only. */
	registrar: string;
}

export function fetchDomains(): Promise<{ domains: DomainLedgerRow[]; spend: DomainSpend }> {
	return get('/api/v1/admin/entry/domains');
}

// The domain screen's figures (roadmap 42 К4). Separate from the ledger
// handle on purpose: the ledger hands out rows and the buttons that act on
// them, this one only counts.

/** The window the domain screen measures over. A name lives for days. */
export type DomainWindow = '7d' | '30d' | '90d';

/** One TLD's line: the window median, and the per-day medians behind it. */
export interface DomainZone {
	tld: string;
	medianHours: number | null;
	sampleCount: number;
	/** One slot per day bucket; null where no name of this zone burned. */
	daily: (number | null)[];
}

/** How long a name survives on a box. Null everywhere means "нет данных". */
export interface DomainLife {
	medianHours: number | null;
	/** Recent half minus older half: negative = names burn faster now. */
	trendHours: number | null;
	sampleCount: number;
	/** ISO day buckets, shared by every zone's line. */
	days: string[];
	zones: DomainZone[];
}

/** The buffer: what is left, how fast it drains, how long that leaves. */
export interface DomainPoolMetrics {
	owned: number;
	assigned: number;
	buying: number;
	burnedWindow: number;
	burnedPerDay: number | null;
	prevBurnedPerDay: number | null;
	hoursLeft: number | null;
}

/**
 * A family of names — how the name is built, not where it lives.
 *
 * The one question the domain screen answers that the overview does not:
 * the overview says how long names live, this says which kind to buy next.
 * A family under the server's sample floor reports its count and no median,
 * because a median over three names is noise dressed as advice.
 */
export interface DomainShape {
	shape: string;
	medianHours: number | null;
	sampleCount: number;
	/** one name of the family, so the label is checkable */
	example: string;
}

/**
 * What the window's names cost, day by day.
 *
 * Counted by the hub and not by the client: the ledger handle caps the rows
 * it hands out, so a sum taken from that list undercounts the moment the pool
 * outgrows the cap.
 */
export interface DomainSpendMetrics {
	boughtCount: number;
	boughtCents: number;
	/** null when nothing was bought: an average over no purchases is not $0 */
	avgCents: number | null;
	days: string[];
	dailyCents: number[];
	dailyCount: number[];
	dailyAvg: (number | null)[];
	/** the calendar month's committed spend — the span the cap is checked over */
	monthSpentCents: number;
}

export interface EntryDomainMetrics {
	window: DomainWindow;
	life: DomainLife;
	/** longest-lived family first; unranked families after the measured ones */
	shapes: DomainShape[];
	pool: DomainPoolMetrics;
	spend: DomainSpendMetrics;
	missing: MissingFigure[];
}

/** The console's wording for the server's shape ids. */
export const DOMAIN_SHAPE_LABEL: Record<string, string> = {
	word_digits: 'слово + цифры',
	long_word: 'длинное слово',
	short_word: 'короткое слово',
	hyphenated: 'через дефис',
	digits: 'только цифры',
	other: 'прочие',
};

export function fetchDomainMetrics(window: DomainWindow = '30d'): Promise<EntryDomainMetrics> {
	return get(`/api/v1/admin/entry/domains/metrics?window=${encodeURIComponent(window)}`);
}

/** One name the registrar quoted, judged against what is left of the domain budget. */
export interface DomainOffer {
	domain: string;
	tld: string;
	available: boolean;
	costCents: number;
	/** dearer than what is left of today's or this month's domain budget */
	overCap: boolean;
}

/** Everything shown before «купить»: the quote, the registrar's dry run, the hub's month. */
export interface DomainCheckout extends DomainOffer {
	wouldSucceed: boolean;
	sufficientFunds: boolean;
	withinRegistrarLimit: boolean;
	balanceCents: number;
	/** the registrar group before this purchase */
	spend: SpendReading;
	withinHubCaps: boolean;
}

export function suggestDomains(n = 3): Promise<{ offers: DomainOffer[] }> {
	return get('/api/v1/admin/entry/domains/suggest', { n });
}

export function checkoutDomain(domain: string): Promise<{ checkout: DomainCheckout }> {
	return post('/api/v1/admin/entry/domains/checkout', { domain });
}

/** Spends money: the server asks for a passkey touch first (step-up). */
export function buyDomain(domain: string): Promise<{ domain: DomainLedgerRow }> {
	return post('/api/v1/admin/entry/domains/buy', { domain });
}

/** A domain on the registrar account, seen against the hub's ledger. */
export interface AccountDomain {
	domain: string;
	tld: string;
	expiresAt?: string;
	/** The ledger status when the hub has it; absent = not in the hub. */
	hubStatus?: DomainStatus;
	box?: string;
	/** Why it may not go into the pool; absent = it may. */
	blocked?: 'brand' | 'excluded' | 'invalid';
}

/** A name the hub never uses for a relay; covers its subdomains too. */
export interface DomainExclusion {
	domain: string;
	note?: string;
	/** The brand's domain from the hub's config; cannot be removed. */
	builtIn: boolean;
	createdAt?: string;
	/** Where a covered name is in the hub now (already pooled or on a box). */
	hubStatus?: DomainStatus;
	box?: string;
}

export function fetchExclusions(): Promise<{ exclusions: DomainExclusion[] }> {
	return get('/api/v1/admin/entry/domains/exclusions');
}

export function excludeDomain(domain: string, note = ''): Promise<{ ok: boolean }> {
	return post('/api/v1/admin/entry/domains/exclusions', { domain, note });
}

/** Makes the name usable again: the server asks for a passkey touch first. */
export function unexcludeDomain(domain: string): Promise<{ ok: boolean }> {
	return del(`/api/v1/admin/entry/domains/exclusions/${encodeURIComponent(domain)}`);
}

export function fetchAccountDomains(): Promise<{ domains: AccountDomain[] }> {
	return get('/api/v1/admin/entry/domains/account');
}

export const ACCOUNT_DOMAIN_BLOCKED: Record<NonNullable<AccountDomain['blocked']>, string> = {
	brand: 'домен бренда, релеям не отдаётся',
	excluded: 'в списке «не трогаем»',
	invalid: 'не годится для релея',
};

export function seedDomain(domain: string): Promise<{ ok: boolean }> {
	return post('/api/v1/admin/entry/domains/seed', { domain });
}

/** Whole cents as dollars: 154 → "$1.54". */
export function formatCents(cents: number): string {
	return `$${(cents / 100).toFixed(2)}`;
}

const MONTHS_SHORT = ['янв', 'фев', 'мар', 'апр', 'май', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек'];

/**
 * A bucket's start as an axis label, in the operator's own time: hours for a
 * bucket under a day, the date otherwise. Every series of the antiblock
 * screens carries RFC 3339 bucket starts and the bucket width beside them.
 */
export function bucketLabel(iso: string, bucketSeconds: number): string {
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return iso;
	if (bucketSeconds < 86_400) return `${String(d.getHours()).padStart(2, '0')}:00`;
	return `${d.getDate()} ${MONTHS_SHORT[d.getMonth()]}`;
}

/** A bucket spelled out for a tooltip: the date, and the hours it covers. */
export function bucketTitle(iso: string, bucketSeconds: number): string {
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return iso;
	const day = `${d.getDate()} ${MONTHS_SHORT[d.getMonth()]}`;
	if (bucketSeconds >= 86_400) return `${day} ${d.getFullYear()}`;
	const end = new Date(d.getTime() + bucketSeconds * 1000);
	const hh = (x: Date) => `${String(x.getHours()).padStart(2, '0')}:00`;
	return `${day}, ${hh(d)}–${hh(end)}`;
}

/** Dollars typed by the owner ("1.5", "2") as whole cents; null when not a sum. */
export function parseDollars(s: string): number | null {
	const t = s.trim().replace(',', '.').replace(/^\$/, '');
	if (!/^\d+(\.\d{1,2})?$/.test(t)) return null;
	return Math.round(Number(t) * 100);
}

// Provider accounts (roadmap 42 П9.1, internal/entry/providers.go): the
// registrar and, later, hosting accounts the hub spends through. Keys are
// sealed on the hub and never come back; the console sees a masked tail and
// the last check. Saving keys asks for a passkey touch (step-up).

export type ProviderRole = 'registrar' | 'hosting';

export interface CredentialField {
	name: string;
	label: string;
	secret: boolean;
	optional?: boolean;
}

export interface ProviderCheck {
	at: string;
	ok: boolean;
	error?: string;
	balanceCents?: number;
	accountSpentCents?: number;
	/** The provider's own monthly cap; absent = it applies none. */
	accountLimitCents?: number;
}

export interface ProviderAccount {
	kind: string;
	title: string;
	role: ProviderRole;
	fields: CredentialField[];
	configured: boolean;
	enabled: boolean;
	/** The one the hub uses now for its role. */
	active: boolean;
	credsHint?: string;
	tlds: string[];
	/** A hoster's order defaults (П9.2); absent on a registrar. */
	defaultRegion?: string;
	defaultPlan?: string;
	check?: ProviderCheck;
	updatedAt?: string;
}

export interface ProviderSave {
	enabled: boolean;
	/** Absent keeps the saved key. */
	credentials?: Record<string, string>;
	tlds: string[];
	/** Only a hoster has these; the hub refuses them on a registrar. */
	defaultRegion?: string;
	defaultPlan?: string;
}

export function fetchProviders(): Promise<{ providers: ProviderAccount[]; domainSpend: DomainSpend }> {
	return get('/api/v1/admin/entry/providers');
}

// The provider comparison (roadmap 42 К6). Its own handle, and read-only: the
// card above it edits keys and counts nothing, this counts and edits nothing.

/** One day of the ban sparkline. Zero is a real zero: no ban that day. */
export interface ProviderBanDay {
	day: string;
	bans: number;
}

/** One hoster, measured. Null is "нет данных", never a flattering default. */
export interface ProviderStats {
	provider: string;
	boughtCount: number;
	aliveCount: number;
	retiredCount: number;
	/** Handed back over a blocked address, versus handed back healthy. */
	bannedCount: number;
	rotatedCount: number;
	/** Machines old enough to be asked whether they survived the window. */
	cohortCount: number;
	survivedCount: number;
	winratePct: number | null;
	/**
	 * Measured over machines the censor burned: a living one is unfinished,
	 * and one handed back on purpose was let go, not burned.
	 */
	medianLifeDays: number | null;
	lifeSampleCount: number;
	monthlyCents: number;
	perBoxMonthlyCents: number | null;
	/** Live machines with no price on record (imported before П9.2). */
	pricelessBoxes: number;
	costPerLivingDayCents: number | null;
	bansDaily: ProviderBanDay[];
	bansWindow: number;
	/** What this hoster's relays carried over the window; null when none pulsed. */
	trafficGb: number | null;
	/** The rent its machines accrued over the same window. */
	spentCents: number;
}

export interface EntryProviderCompare {
	windowDays: number;
	hosting: ProviderStats[];
	missing: MissingFigure[];
}

export function fetchProviderCompare(): Promise<EntryProviderCompare> {
	return get('/api/v1/admin/entry/providers/compare');
}

export function saveProvider(kind: string, body: ProviderSave): Promise<{ provider: ProviderAccount }> {
	return put(`/api/v1/admin/entry/providers/${encodeURIComponent(kind)}`, body);
}

/** Catalog for this hoster, using draft credentials or its saved key. Never saves or enables it. */
export interface ProviderCatalog {
	kind: string;
	regions: BoxRegion[];
	plans: BoxPlan[];
}

export function previewProviderCatalog(
	kind: string,
	credentials?: Record<string, string>,
): Promise<{ catalog: ProviderCatalog }> {
	return post(`/api/v1/admin/entry/providers/${encodeURIComponent(kind)}/catalog`, { credentials });
}

export function checkProvider(kind: string): Promise<{ provider: ProviderAccount }> {
	return post(`/api/v1/admin/entry/providers/${encodeURIComponent(kind)}/check`);
}

/**
 * A provider call's failure in words the owner can act on: the hub's own
 * refusals (over a cap, no registrar, a bad name) and the provider's answer
 * (a revoked key, no credit) come as the server's text.
 */
export function describeProviderError(e: unknown): string {
	if (e instanceof ApiError && e.code && [400, 409, 422, 502].includes(e.status)) {
		const why = e.code.replace(/^entry: (provider check failed: |rejected: )?/, '');
		if (e.status === 422) return `ключ не прошёл проверку у провайдера: ${why}`;
		if (e.status === 502) return `провайдер ответил ошибкой: ${why}`;
		return why;
	}
	return describeApiError(e);
}

export const PROVIDER_ROLE_LABEL: Record<ProviderRole, string> = {
	registrar: 'регистратор доменов',
	hosting: 'хостинг машин',
};

export const DOMAIN_STATUS_LABEL: Record<DomainStatus, string> = {
	buying: 'покупается',
	owned: 'в пуле',
	assigned: 'на машине',
	burned: 'под вопросом',
	dead: 'мёртв · не продлевать',
	failed: 'не куплен',
};

/**
 * A box name for a city: its first latin letters and the next free number
 * (amsterdam → ams1, then ams2). Empty when the city is not written in latin.
 */
export function suggestBoxName(location: string, taken: string[]): string {
	const letters = location
		.toLowerCase()
		.replace(/[^a-z]/g, '')
		.slice(0, 3);
	if (!letters) return '';
	let n = 1;
	while (taken.includes(`${letters}${n}`)) n++;
	return `${letters}${n}`;
}

// Why a relay died (internal/entry DeathIPBanned…, migration 051): every death
// says it, it is the label the abuse model learns from (П6.7, П6.12).
export type DeathReason =
	| 'ip_banned'
	| 'fingerprint'
	| 'destination'
	| 'provider_down'
	| 'rotated'
	| 'domain_blocked'
	| 'other';

export const DEATH_REASONS: { id: DeathReason; label: string; hint: string }[] = [
	{ id: 'ip_banned', label: 'адрес заблокирован', hint: 'из рф не открывается ни туннель, ни голый tls на адрес' },
	{ id: 'fingerprint', label: 'режут протокол', hint: 'адрес открывается, туннель нет' },
	{ id: 'destination', label: 'сломалось то, что за релеем', hint: 'релей жив, прикрытие или гейтвей за ним нет' },
	{ id: 'provider_down', label: 'машина умерла', hint: 'провайдер, железо, сеть. цензура ни при чём' },
	{ id: 'rotated', label: 'плановая замена', hint: 'был здоров, снят нарочно' },
	{ id: 'domain_blocked', label: 'заблокирован домен', hint: 'xhttp: адрес открывается, имя нет. нужен новый домен' },
	{ id: 'other', label: 'другое', hint: '' },
];

export const deathLabel = (r?: string): string => DEATH_REASONS.find((d) => d.id === r)?.label ?? '';

/** The reason the prober's last verdict points at, if it points at one. */
export function reasonFromVerdict(v?: CensorVerdict): DeathReason | null {
	if (v === 'ip_banned') return 'ip_banned';
	if (v === 'fingerprint') return 'fingerprint';
	if (v === 'domain_blocked') return 'domain_blocked';
	return null;
}

export function markRelayDead(name: string, reason: DeathReason): Promise<{ revokedGrants: number }> {
	return post(`/api/v1/admin/entry/relays/${encodeURIComponent(name)}/dead`, { reason });
}

/**
 * Hand a box back (П6.10): its relays die at once with one reason, then a job
 * wipes the machine. wipeError: retired, but the wipe did not start.
 */
export function retireBox(
	name: string,
	reason: DeathReason,
): Promise<{
	relays: string[];
	holders: number;
	jobId?: string;
	wipeError?: string;
	/** The machine was deleted at the hoster, which is what stops the charge. */
	instanceDeleted?: boolean;
	instanceError?: string;
}> {
	return post(`/api/v1/admin/entry/boxes/${encodeURIComponent(name)}/retire`, { reason });
}

/**
 * The hoster under key (roadmap 42 П9.2): what it sells, where, the saved
 * order defaults and the fleet's monthly bill against the cap.
 */
export interface BoxRegion {
	id: string;
	city: string;
	country: string;
}

export interface BoxPlan {
	id: string;
	vcpus: number;
	ramMb: number;
	diskGb: number;
	bandwidthGb: number;
	monthlyCents: number;
	/** Where this plan can be ordered; a plan sold out of a region fails. */
	regions: string[];
}

export interface HostingCatalog {
	kind: string;
	regions: BoxRegion[];
	plans: BoxPlan[];
	defaultRegion?: string;
	defaultPlan?: string;
	/** the hosting group against its caps: what an order is checked against */
	spend: SpendReading;
}

/** One machine at the hoster next to the hub's record of it. */
export interface HostingInstance {
	id: string;
	label: string;
	address: string;
	region: string;
	plan: string;
	status: string;
	monthlyCents: number;
	/** The box this instance belongs to; empty when no row names it. */
	box?: string;
	/** The hub is paying for a machine it has no box for, or whose box it retired. */
	orphan: boolean;
	/** The box was retired and the hoster has not deleted the machine yet. */
	retired?: boolean;
	tags?: string[];
}

export function fetchHosting(): Promise<{ hosting: HostingCatalog }> {
	return get('/api/v1/admin/entry/hosting');
}

export function fetchHostingInstances(): Promise<{ instances: HostingInstance[] }> {
	return get('/api/v1/admin/entry/hosting/instances');
}

export interface BoxOrder {
	region?: string;
	plan?: string;
	/** Derived from the region's city when empty. */
	name?: string;
	proto?: string;
	voiceRegion?: string;
}

/** Rent a machine: the hub orders it, waits for its address and provisions it. */
export function orderBox(body: BoxOrder): Promise<{ box: string; jobId: string }> {
	return post('/api/v1/admin/entry/boxes/order', body);
}

/** Restart a machine through the hoster and wait for it to answer, as a job. */
export function rebootBox(name: string): Promise<{ jobId: string }> {
	return post(`/api/v1/admin/entry/boxes/${encodeURIComponent(name)}/reboot`);
}

/** Hand a retired box's machine back; the retire does this itself when it can. */
export function deleteBoxInstance(name: string): Promise<{ ok: boolean }> {
	return del(`/api/v1/admin/entry/boxes/${encodeURIComponent(name)}/instance`);
}

/** Run the wipe of a retired box again. */
export function wipeBox(name: string): Promise<{ jobId: string }> {
	return post(`/api/v1/admin/entry/boxes/${encodeURIComponent(name)}/wipe`);
}

/**
 * A refused request says why in its `error` (which rule); that text is worth
 * more to the operator than a generic one.
 */
export function describeEntryError(e: unknown): string {
	if (e instanceof ApiError && e.status === 400 && e.code) return e.code;
	if (e instanceof ApiError && e.status === 404) return e.code || 'не найдено';
	if (e instanceof ApiError && e.status === 409)
		return e.code?.includes('retired') ? 'машина сдана' : 'на машине уже идёт задача';
	return describeApiError(e);
}

// The Censor surface (roadmap 42 П6.6, internal/admin/censor.go). The latest
// liveness verdict per relay from the RU prober — what cmd/entry-prober last
// saw and why — joined on the client with the pool above by relay name.

export type CensorVerdict =
	| 'clean'
	| 'ip_banned'
	| 'fingerprint'
	| 'reality_wins'
	| 'network_down'
	| 'domain_blocked'
	| 'unknown';

export interface RelayVerdict {
	relayName: string;
	sourceRegion: string;
	verdict: CensorVerdict;
	/** 1/0 flags: the §6 signals — reality tunnel, bare TLS to the IP, allowed-site control. */
	realityOk: number;
	k1BareTls: number;
	k2Control: number;
	realityRttMs: number;
	cover: string;
	/** unix seconds of the latest probe for this relay in the window. */
	lastSeen: number;
}

export type CensorRange = '15m' | '1h' | '6h' | '24h';

export function fetchCensorProbes(range: CensorRange = '24h'): Promise<{ range: string; relays: RelayVerdict[] }> {
	return get(`/api/v1/admin/censor/probes?range=${encodeURIComponent(range)}`);
}

/** One bucket of a relay's probe history (internal/admin/censor.go CensorSeries). */
export interface CensorPoint {
	ts: number;
	/** share of probes through the tunnel, 0..100 */
	reality: number;
	/** share of bare TLS handshakes to the address, 0..100 */
	k1: number;
	/** mean tunnel RTT of the probes that got through; null when none did */
	rttMs: number | null;
	verdict: CensorVerdict;
}

export interface CensorSeries {
	relayName: string;
	sourceRegion: string;
	points: CensorPoint[];
}

export function fetchCensorSeries(
	range: CensorRange,
): Promise<{ range: string; bucket: string; series: CensorSeries[] }> {
	return get(`/api/v1/admin/censor/series?range=${encodeURIComponent(range)}`);
}

// ── The console's first screen (roadmap 42 П9.9) ────────────────────────────
//
// One request for the whole dashboard. It only reads: nothing on that screen
// acts, and the pages that act compute no analytics.

/** One figure the hub cannot compute yet, with the reason. */
export interface MissingFigure {
	figure: string;
	why: string;
}

/** One day of the doorway chart. */
/**
 * One bucket of the overview's reach series. Both shares are null for a
 * bucket nobody attempted in: the chart breaks there rather than drawing a
 * share of nothing.
 */
export interface ReachPoint {
	bucket: string;
	attempts: number;
	/** share that got through at all, 0..100 */
	anyPct: number | null;
	/** share that got through without the race working relays, 0..100 */
	firstPct: number | null;
}

/**
 * Did people get through. The three shares partition every attempt, which is
 * what lets the chart be read as a whole.
 */
export interface ReachBlock {
	attempts: number;
	people: number;
	firstTryPct: number;
	retriedPct: number;
	failedPct: number;
	series: ReachPoint[];
}

/** One spending article in three readings. */
export interface SpendLine {
	perDayCents: number;
	monthCents: number;
	/** where the month lands at the rate seen now */
	projectionCents: number;
}

export interface MoneyBlock {
	domains: SpendLine;
	/** The rented machines, accrued by the day (П9.2). */
	machines: SpendLine;
	/** Both articles together — the whole doorway bill. */
	total: SpendLine;
	monthlyCapCents: number;
	/** Machines in service with no price: the machine line understates by their rent. */
	machinesUnpriced: number;
	/** Machines the hub knows the rent of; zero means the article is not a figure at all. */
	machinesPriced: number;
	/** null when nobody reached in the window: a spend over nobody is no number */
	perUserMonthCents: number | null;
	activePeople: number;
	series: MoneyPoint[];
}

/** One bucket of spend, as a day's rate. */
export interface MoneyPoint {
	bucket: string;
	/** rent of the machines in service during the bucket, per day */
	machinesPerDayCents: number;
	/** paid for names inside the bucket: an event, not scaled to a day */
	domainsCents: number;
	totalCents: number;
	/** distinct people of the 24 hours ending at the bucket's end */
	people: number | null;
	perUserMonthCents: number | null;
}

/**
 * How long what we buy lives before the censor burns it. Censor deaths only:
 * a machine handed back on purpose was let go, not burned.
 */
export interface SurvivalBlock {
	boxMedianDays: number | null;
	boxMedianDaysBefore: number | null;
	/** relays the censor killed in the window */
	censorDeaths: number;
	domainMedianHours: number | null;
	domainMedianHoursBefore: number | null;
	relayMedianDays: number | null;
	relayMedianDaysBefore: number | null;
	relaysEver: number;
	series: SurvivalPoint[];
}

/** What the censor burned inside one bucket; medians null without a death. */
export interface SurvivalPoint {
	bucket: string;
	deaths: number;
	boxMedianDays: number | null;
	relayMedianDays: number | null;
	domainMedianDays: number | null;
}

export interface ProtocolBlock {
	proto: string;
	attempts: number;
	sharePct: number;
	p50Ms: number;
	p95Ms: number;
	connectP50Ms: number;
	/** measured p50 per bucket over the window; empty when the dataset cannot be asked */
	latency: LatencyPoint[];
}

/**
 * One bucket of a protocol's latency series. `p50Ms` is null for a bucket
 * nobody reported through — the chart breaks the line there rather than
 * drawing a median nobody measured.
 */
export interface LatencyPoint {
	bucket: string;
	p50Ms: number | null;
	attempts: number;
}

/**
 * What the relays carried. Per person is a division, never an attribution:
 * the relays report totals and know nothing about who made them, and the
 * people are counted from the clients' own telemetry.
 */
export interface TrafficBlock {
	gbTotal: number;
	/** what clients sent */
	gbUp: number;
	/** what clients received */
	gbDown: number;
	/** null when nobody reached in the window */
	gbPerPersonDay: number | null;
	relaysReporting: number;
	series: TrafficPoint[];
}

/** What the relays carried inside one bucket; null when none pulsed in it. */
export interface TrafficPoint {
	bucket: string;
	gbUp: number | null;
	gbDown: number | null;
	gbPerPersonDay: number | null;
}

/** One incident as the console's right-hand column shows it. */
export interface IncidentRow {
	id: string;
	box: string;
	cause: string;
	status: 'open' | 'acting' | 'resolved' | 'dismissed';
	advice: string;
	evidence?: Record<string, string>;
	playbook?: string;
	/** the repair costs money, which is why it waits for a person */
	spends: boolean;
	/** how far the hub may go alone with this cause */
	mode: 'auto' | 'ask' | 'off';
	why: string;
	/** why the hub stopped working on it by itself; empty while it has not */
	handoff?: string;
	decidedBy?: string;
	costCents: number;
	outcome?: string;
	openedAt: string;
	closedAt?: string;
}

export interface EntryOverview {
	window: OverviewWindow;
	/** width of one point of every series on the screen */
	bucketSeconds: number;
	reach: ReachBlock;
	money: MoneyBlock;
	survival: SurvivalBlock;
	protocols: ProtocolBlock[];
	/** null until some relay has pulsed in the window */
	traffic: TrafficBlock | null;
	/** per person, from each account's socket; null until one has flushed */
	trafficPerUser: TrafficPerUser | null;
	/** what the autopilot cannot finish alone: a queue, kept short */
	needsYou: IncidentRow[];
	/** what it did: a log, and its growing is the good sign */
	automation: IncidentRow[];
	missing: MissingFigure[];
}

export type OverviewWindow = '24h' | '7d' | '30d';

/**
 * What one person moves, read per account and taken as medians. The message
 * socket only: voice and files have no per-account count.
 */
export interface TrafficPerUser {
	people: number;
	medianMbDay: number;
	p90MbDay: number;
	viaRelayPeople: number;
	/** the bypass load of a person who came through a relay; null when nobody did */
	viaRelayMedianMbDay: number | null;
	viaRelayPct: number;
}

/** One repair attempt against an incident (roadmap 42 П9.6). */
export interface IncidentRepair {
	id: string;
	incidentId: string;
	box: string;
	cause: string;
	playbook: string;
	decidedBy: 'auto' | 'ask' | 'manual';
	status: 'running' | 'done' | 'failed';
	error?: string;
	costCents: number;
	startedAt: string;
	finishedAt?: string;
}

/**
 * Run a repair against this incident. `playbook` is the "another action"
 * button: a repair other than the proposed one, chosen from what the hub has
 * code for. Empty means the proposal.
 *
 * Behind a step-up on the server: the repairs that wait for a person are the
 * ones that spend money.
 */
export function runIncidentRepair(id: string, playbook = ''): Promise<{ incident: IncidentRow }> {
	return post(`/api/v1/admin/entry/incidents/${encodeURIComponent(id)}/run`, { playbook });
}

/** One wave fuse, standing or lifted (roadmap 42 К2). */
export interface CausePause {
	id: string;
	cause: string;
	/** the hub's own sentence about why it stood down */
	reason: string;
	/** how many machines the cause had broken when the fuse blew */
	boxes: number;
	pausedAt: string;
	liftedAt?: string;
	liftedBy?: string;
}

/** One repair the hub can be asked to perform, with the cause it answers. */
export interface PlaybookOption {
	name: string;
	mode: 'auto' | 'ask' | 'off';
	spends: boolean;
	why: string;
	cause: string;
}

/** The four endings the automation chart stacks, plus what has not ended. */
export interface ResolutionCounts {
	auto: number;
	handed: number;
	failed: number;
	dismissed: number;
	working: number;
}

export interface IncidentDay extends ResolutionCounts {
	day: string;
}

export interface AutomationBlock {
	/** share of the window's ended incidents the hub closed alone; null over nothing */
	autoPct: number | null;
	autoPctBefore: number | null;
	total: number;
	by: ResolutionCounts;
	days: IncidentDay[];
}

/**
 * The three medians, in seconds. Each is null when the window holds nothing to
 * measure, and the sample counts say how much to trust it.
 */
export interface StagesBlock {
	/** first failing probe → incident opened; the one figure that needs ClickHouse */
	toNoticeSec: number | null;
	/** incident opened → a repair started */
	toDecideSec: number | null;
	/** repair started → the replacement passed a probe and entered the pool */
	toRepairSec: number | null;
	noticeSamples: number;
	decideSamples: number;
	repairSamples: number;
}

/** How an incident ended, as the chart and the feed's filters name it. */
export type IncidentResolution = 'auto' | 'handed' | 'failed' | 'dismissed' | 'working';

/** One incident with everything the timeline needs. */
export interface IncidentFeedRow extends IncidentRow {
	actedAt?: string;
	/** when the replacement passed a probe and entered the pool */
	admittedAt?: string;
	resolution: IncidentResolution;
	repairs: number;
	repairsFailed: number;
}

export interface EntryIncidents {
	window: OverviewWindow;
	automation: AutomationBlock;
	stages: StagesBlock;
	/** the wave fuses standing now: why three look-alike incidents sit untouched */
	pauses: CausePause[];
	needsYou: IncidentFeedRow[];
	feed: IncidentFeedRow[];
	playbooks: PlaybookOption[];
	missing: MissingFigure[];
}

/** The whole incidents screen in one request. */
export function fetchIncidents(window: OverviewWindow = '7d'): Promise<EntryIncidents> {
	return get(`/api/v1/admin/entry/incidents?window=${encodeURIComponent(window)}`);
}

/** The pool as it stood at the end of one bucket of the window. */
export interface FleetPoolPoint {
	bucket: string;
	/** relays that existed and had not died by then */
	size: number;
	boxes: number;
}

export interface FleetPool {
	inDistribution: number;
	boxesCount: number;
	pendingCount: number;
	rollingCount: number;
	series: FleetPoolPoint[];
}

/** Censor deaths against relays signed into the pool, in one bucket. */
export interface DeathRepBucket {
	bucket: string;
	deaths: number;
	replacements: number;
}

/**
 * A death here is a censor death: a machine handed back on purpose loses its
 * relays too, and that is no block.
 */
export interface FleetDeathReplacement {
	deathsDay: number;
	replacementsDay: number;
	series: DeathRepBucket[];
}

export interface FleetTTR {
	medianSec: number | null;
	peakSec: number | null;
	sampleCount: number;
}

export interface FleetStrip {
	deathsVsReplacements: FleetDeathReplacement;
	timeToReplacement: FleetTTR;
}

export interface FleetJob {
	id: string;
	box: string;
	kind: string;
	status: 'running' | 'done' | 'failed';
	step: string;
	error?: string;
	createdAt: string;
	finishedAt?: string;
	canRetry: boolean;
}

export interface FleetRelay {
	name: string;
	/** opened from the relay's sealed identity; empty when unreadable */
	proto: string;
	port: number;
	cover: string;
	/** the server key could not open the identity: no port, proto or cover */
	unreadable?: boolean;
	grants: number;
	signedAt: string;
	status: RelayStatus;
	verdict: string;
	kind: string;
	diedAt?: string;
}

/** One machine as the list shows it. No address: that is opened on the card. */
export interface FleetBox {
	name: string;
	provider: string;
	location: string;
	proto: string;
	region?: string;
	rentedUntil?: string;
	createdAt: string;
	retiredAt?: string;
	domain?: string;
	arch?: string;
	edgeSha?: string;
	openIncident?: string;
	verdict: 'ok' | 'warn' | 'bad' | 'mute';
	verdictLabel: string;
	relays: FleetRelay[];
	monthlyCents?: number;
	plan?: string;
	providerRegion?: string;
}

export interface EntryFleet {
	window: OverviewWindow;
	/** width of one point of the pool and strip series */
	bucketSeconds: number;
	pool: FleetPool;
	strip: FleetStrip;
	jobs: FleetJob[];
	boxes: FleetBox[];
	/** the provider comparison, carried along so the screen is one request */
	providers: EntryProviderCompare;
	cleanBoxesCount: number;
	cleanRelaysCount: number;
	retiredBoxesCount: number;
	missing: MissingFigure[];
}

/** The whole fleet screen in one request (roadmap 42 К1). */
export function fetchFleet(window: OverviewWindow = '7d'): Promise<EntryFleet> {
	return get(`/api/v1/admin/entry/machines?window=${encodeURIComponent(window)}`);
}

// ── The machine card (roadmap 42 П9.3) ─────────────────────────────────────

export interface BoxIncident {
	id: string;
	cause: string;
	status: string;
	playbook?: string;
	outcome?: string;
	costCents: number;
	openedAt: string;
	closedAt?: string;
}

/** A machine's life, every figure computed from the rows. */
export interface BoxHistory {
	bornAt: string;
	/** its first relay's signing into the pool; null if none ever was */
	admittedAt: string | null;
	retiredAt: string | null;
	retiredReason?: DeathReason;
	/** birth to retirement, or to now */
	lifetimeSec: number;
	/** rent over the life; null for a machine rented by hand */
	rentCents: number | null;
	repairCents: number;
	incidents: BoxIncident[];
}

export interface BoxCardRelay extends FleetRelay {
	diedReason?: DeathReason;
	/** the latest pulse in the window; absent when it never pulsed */
	lastPulseAt?: string;
	version?: string;
	uptimeS?: number;
}

/** One bucket of the pulse; every field null where no relay pulsed. */
export interface BoxStatsPoint {
	bucket: string;
	mbUp: number | null;
	mbDown: number | null;
	connsPerSec: number | null;
	cpuPct: number | null;
	memMb: number | null;
}

export interface BoxCardInfo {
	name: string;
	/** opened from the seal for the card only */
	address?: string;
	addressUnreadable?: boolean;
	provider: string;
	location: string;
	region?: string;
	providerRegion?: string;
	plan?: string;
	proto: string;
	arch?: string;
	edgeSha?: string;
	domain?: string;
	monthlyCents?: number;
}

export interface EntryBoxCard {
	window: OverviewWindow;
	bucketSeconds: number;
	box: BoxCardInfo;
	history: BoxHistory;
	relays: BoxCardRelay[];
	/** null when no relay of the machine pulsed in the window */
	stats: { series: BoxStatsPoint[] } | null;
	/** accounts whose socket came through the machine; null when none */
	people: number | null;
	missing: MissingFigure[];
}

/** One machine's card in one request. Owner-only: it opens the address. */
export function fetchBoxCard(name: string, window: OverviewWindow = '7d'): Promise<EntryBoxCard> {
	return get(`/api/v1/admin/entry/machines/${encodeURIComponent(name)}?window=${encodeURIComponent(window)}`);
}

/**
 * Lift a wave fuse: a person looked at the wave and tells the autopilot to
 * carry on. Hands the cause's incidents back with it.
 *
 * Behind a step-up, like running a repair: lifting the pause on a cause whose
 * repair spends money is what lets the hub spend it again.
 */
export function liftCausePause(cause: string): Promise<{ ok: boolean; freed: number }> {
	return post(`/api/v1/admin/entry/pauses/${encodeURIComponent(cause)}/lift`);
}

// ── The detection screen (roadmap 42 К3) ───────────────────────────────────
//
// Two halves, deliberately apart: the ban tempo is analytics and read-only,
// the eviction thresholds act and are written through internal/entry.

/** One bucket of the ban tempo. */
export interface PressurePoint {
	ts: number;
	domainBlocked: number;
	ipBanned: number;
	fingerprint: number;
	/** probe rows in the bucket; 0 means nobody looked, not that it was calm */
	probes: number;
}

/**
 * One observation point over the window: how much it looked, at how many
 * relays, when it last reported, and its probes per bucket.
 *
 * Everything else on the detection screen is "what the probers saw", so a
 * prober that died makes the censor look like it stopped pushing.
 */
export interface PressureVantage {
	region: string;
	title?: string;
	box?: string;
	host?: string;
	provider?: string;
	status?: 'active' | 'retired';
	createdAt?: string;
	retiredAt?: string;
	unreadable?: boolean;
	silent?: boolean;
	probes: number;
	relays: number;
	/** unix seconds of its newest probe row (0 when never seen) */
	lastSeen: number;
	/** the verdict of that newest row, whatever the window */
	lastVerdict?: string;
	/** one count per point of the series, in the same order */
	perBucket: number[];
}

/** One observation point slot in Postgres (`entry_vantages`, migration 068). */
export interface VantageSlot {
	slot: string;
	title: string;
	box?: string;
	host?: string;
	provider: string;
	status: 'active' | 'retired';
	createdAt: string;
	retiredAt?: string;
	updatedAt: string;
	updatedBy: string;
	unreadable?: boolean;
	jobId?: string;
}

/**
 * A slot as a token-issuing route answers it (admin_vantages.go). A slot on a
 * hub machine gets its prober deployed (`jobId`) and its token planted there:
 * the answer never carries it. A standalone slot gets `token` and the `env`
 * its prober reads in this one answer; the hub keeps only the hash.
 */
export interface VantageIssued extends VantageSlot {
	token?: string;
	env?: string;
	/** why the prober on a hub machine was not deployed; the slot was saved */
	deployError?: string;
}

/** Either `box` (a hub machine without relays) or `host` (a standalone VPS). */
export interface VantageSpecInput {
	slot?: string;
	title?: string;
	box?: string;
	host?: string;
	provider?: string;
}

export function fetchVantages(): Promise<{ vantages: VantageSlot[] }> {
	return get('/api/v1/admin/entry/vantages');
}

/** Open a slot (behind step-up: it issues the slot's token). */
export function createVantage(spec: VantageSpecInput): Promise<VantageIssued> {
	return post('/api/v1/admin/entry/vantages', spec);
}

/** Replace the machine inside a slot (behind step-up). */
export function saveVantage(slot: string, spec: Omit<VantageSpecInput, 'slot'>): Promise<VantageIssued> {
	return put(`/api/v1/admin/entry/vantages/${encodeURIComponent(slot)}`, { slot, ...spec });
}

/**
 * A new token for a slot; the old one stops working (behind step-up). A slot
 * on a hub machine is redeployed, a standalone one gets the token once.
 */
export function reissueVantageToken(slot: string): Promise<VantageIssued> {
	return post(`/api/v1/admin/entry/vantages/${encodeURIComponent(slot)}/token`, {});
}

/** Retire a slot (behind step-up). */
export function retireVantage(slot: string): Promise<VantageSlot> {
	return del(`/api/v1/admin/entry/vantages/${encodeURIComponent(slot)}`);
}

export interface PressureRate {
	total: number;
	domainBlocked: number;
	ipBanned: number;
	fingerprint: number;
}

export interface PressureCount {
	total: number;
	domainBlocked: number;
	ipBanned: number;
	fingerprint: number;
}

/** One blown fuse: a cause the autopilot stood down on (К2). */
export interface PressureWave {
	cause: string;
	reason: string;
	boxes: number;
	pausedAt: number;
	lifted: boolean;
}

export interface EntryPressure {
	range: CensorRange;
	bucketSeconds: number;
	points: PressurePoint[];
	rate: PressureRate;
	/** the window before this one; null when the dataset does not reach back */
	before: PressureRate | null;
	bans: PressureCount;
	waves: PressureWave[];
	vantages: PressureVantage[];
	missing: MissingFigure[];
}

/**
 * The ban tempo by cause, with the waves read from the fuse's own record.
 *
 * A ban here is a transition into a blocking verdict, not a probe that said
 * "banned": counting rows would draw one dead relay as a rising slope.
 */
export function fetchPressure(range: CensorRange = '6h'): Promise<EntryPressure> {
	return get(`/api/v1/admin/entry/pressure?range=${encodeURIComponent(range)}`);
}

/** The eviction thresholds as the hub holds them (migration 065). */
export interface DetectionPolicy {
	failStrikes: number;
	waveLimit: number;
	waveWindowSeconds: number;
	verdictWindowSeconds: number;
	repairCooldownSeconds: number;
	requireTwoVantages: boolean;
	ignoreNetworkDown: boolean;
	/** the second step of each consumable's life, by kind (migration 073) */
	secondChance: Record<SuspectKind, SecondChance>;
	updatedAt: string;
	updatedBy: string;
	/** the observation points that reported lately */
	vantages: string[];
	/** switches the hub refuses to move, with the reason the screen shows */
	locked: Record<string, string>;
}

/** What gets a second chance: a domain, a machine (its address), a cover site. */
export type SuspectKind = 'domain' | 'box' | 'cover';

/**
 * How a suspect — out of the pool on the eviction threshold, not yet written
 * off — is reprobed, returned and written off (roadmap 42 П9.11).
 */
export interface SecondChance {
	/** how often one suspect is reprobed */
	probeEverySeconds: number;
	/** clean reprobes in a row that return it to the pool */
	returnClean: number;
	/** failed reprobes that write it off */
	deadFails: number;
	/** how long it may stay suspect at all */
	deadAfterSeconds: number;
}

/** One suspicion as the hub's ledger holds it (migration 073). */
export interface Suspect {
	id: string;
	kind: SuspectKind;
	/** the domain, the machine's name, or the cover site's host */
	subject: string;
	/** the cause that evicted it */
	reason: string;
	/** withdrawn: the machine was reinstalled by the operator (migration 078) */
	status: 'suspect' | 'returned' | 'dead' | 'withdrawn';
	since: string;
	lastProbeAt?: string;
	cleanStreak: number;
	failedProbes: number;
	endedAt?: string;
	outcome?: string;
}

/** What is under suspicion now, then what ended in the last thirty days. */
export function fetchSuspects(): Promise<{ suspects: Suspect[] }> {
	return get('/api/v1/admin/entry/suspects');
}

export function fetchDetectionPolicy(): Promise<DetectionPolicy> {
	return get('/api/v1/admin/entry/policy');
}

/**
 * Turn one knob. Only the fields that moved are sent: a save of one stepper
 * must not reset another.
 *
 * Behind a step-up on the server — a lower strike count retires machines
 * sooner, a wider wave window keeps the autopilot buying through a rule change
 * at the censor.
 */
export function saveDetectionPolicy(patch: {
	failStrikes?: number;
	waveLimit?: number;
	waveWindowSeconds?: number;
	verdictWindowSeconds?: number;
	repairCooldownSeconds?: number;
	requireTwoVantages?: boolean;
	/** per kind, only the knobs that moved */
	secondChance?: Partial<Record<SuspectKind, Partial<SecondChance>>>;
}): Promise<DetectionPolicy> {
	return put('/api/v1/admin/entry/policy', patch);
}

export type PlaybookMode = 'auto' | 'ask' | 'off';

/** One cause as the autopilot's modes panel lists it (migration 069). */
export interface PlaybookModeRow {
	cause: string;
	playbook: string;
	mode: PlaybookMode;
	/** the code's own default, which the owner's mode departs from */
	defaultMode: PlaybookMode;
	/** the repair costs money */
	spends: boolean;
	why: string;
}

export function fetchPlaybookModes(): Promise<{ modes: PlaybookModeRow[] }> {
	return get('/api/v1/admin/entry/playbook-modes');
}

/** Behind a step-up: `auto` on a repair that spends is a purchase made later. */
export function savePlaybookMode(cause: string, mode: PlaybookMode): Promise<{ modes: PlaybookModeRow[] }> {
	return put(`/api/v1/admin/entry/playbook-modes/${encodeURIComponent(cause)}`, { mode });
}

/** One pool with its target and forecast (internal/entry/fleet.go, П9.10). */
export interface FleetPool {
	/** "<proto>" or "hysteria2:<region>" */
	pool: string;
	proto: string;
	voiceRegion?: string;
	/** live machines the pool's load asks for (П9.16), derived by the hub */
	target: number;
	/** machines it holds on to before handing one back (П9.17), ≥ target */
	keep: number;
	/** how the target was derived, in the hub's words */
	targetWhy: string;
	/** what the target was read from */
	sizing: PoolSizing;
	/** machines in service with a relay carrying people, the leaving ones included */
	live: number;
	/** machines being emptied for a handback */
	leaving: number;
	losses: number;
	exposureHours: number;
	/** losses per machine per day; 0 with too little history */
	lossPerBoxDay: number;
	/** twice the measured median from an order to admission; 0 with no order yet */
	horizonSeconds: number;
	/** machines beyond the target the forecast asks for */
	buffer: number;
	/** how many the pool is short by, now and by the forecast; ≤ 0 is fine */
	need: number;
	why: string;
}

/** The domain pool against the target the hub derives (internal/entry/domainpool.go, П9.19). */
export interface DomainPool {
	/** incoming + buffer, at least one while the xhttp pool keeps a machine */
	target: number;
	/** names on machines now */
	inUse: number;
	/** xhttp machines the fleet is about to order */
	incoming: number;
	/** names burnt over the last 30 days, and the stock they ask for a day ahead */
	burns: number;
	buffer: number;
	/** owned, unassigned, not excluded */
	free: number;
	/** purchases whose answer never came — counted in the month, retried before any new one */
	unknown: number;
	need: number;
	why: string;
}

/** The fleet panel's whole reading: the pools, the domain pool, what the owner must not miss. */
/** A pool's forecast traffic up to the horizon (internal/entry/demand.go, П9.14). */
export interface PoolDemand {
	nowMbps: number;
	forecastMbps: number;
	peakAt: string;
	slopeMbpsPerDay: number;
	historyDays: number;
	basis: 'none' | 'flat' | 'weekly';
	lastAt: string;
	fresh: boolean;
	/** live relays that have not reported lately */
	silent?: string[];
	complete: boolean;
	why: string;
}

/** What one machine of a plan carries (internal/entry/capacity.go, П9.15). */
export interface PlanCapacity {
	provider: string;
	plan: string;
	mbps: number;
	source: 'rtt' | 'cpu' | 'floor' | 'none';
	boxes: number;
	machines: number;
	why: string;
}

export interface PoolSizing {
	demand?: PoolDemand;
	capacity?: PlanCapacity;
}

export interface FleetReading {
	pools: FleetPool[];
	domainPool: DomainPool;
	/** e.g. a pool that needs domains with no registrar to buy them */
	warnings: string[];
	capacities: PlanCapacity[];
}

export function fetchFleetPools(): Promise<FleetReading> {
	return get('/api/v1/admin/entry/fleet');
}

/** A machine coming into a pool or leaving it. */
export interface FleetEvent {
	at: string;
	box: string;
	kind: 'in' | 'leaving' | 'out';
	reason?: string;
}

/** One hour of a line (internal/entry/poolchart.go, П9.19). */
export interface ChartPoint {
	at: string;
	v: number;
}

/** A line: the past solid, the next day dashed; the forecast starts where the history ends. */
export interface ChartLine {
	history: ChartPoint[];
	forecast: ChartPoint[];
}

/** One pool's lines, or the whole doorway's (pool empty). Never empty: a flat zero is a line. */
export interface ChartSet {
	pool: string;
	/** an hour's busiest five minutes */
	loadMbps: ChartLine;
	machines: ChartLine;
	/** what the machines alive at that hour cost a day */
	machinesCentsPerDay: ChartLine;
	/** domains held, and what was spent on them over the 24 hours to each point; only where domains are used */
	domains?: ChartLine;
	domainsCentsPerDay?: ChartLine;
	/** what one machine carries; 0 when unmeasured */
	machineMbps: number;
	/** the share of a machine the hub plans to run at */
	headroom: number;
	events: FleetEvent[];
	why?: string;
}

/** Every chart of the autopilot screen. */
export interface FleetChart {
	now: string;
	total: ChartSet;
	pools: ChartSet[];
	spend: SpendReading[];
}

export function fetchFleetChart(): Promise<FleetChart> {
	return get('/api/v1/admin/entry/fleet/chart');
}

// The owner's one knob (internal/entry/spend.go, П9.19): what each spend group
// may spend in a day and in a month. Everything under it the hub does by
// itself.

export type SpendRole = 'hosting' | 'registrar';

/** One spend group against its caps. */
export interface SpendReading {
	role: SpendRole;
	dailyCapCents: number;
	monthlyCapCents: number;
	updatedAt: string;
	/** accrued so far */
	dayCents: number;
	monthCents: number;
	/** where the period ends if everything running now keeps running: what the gate reads */
	dayProjectedCents: number;
	monthProjectedCents: number;
	/** what the machines running now cost an hour; 0 for the registrars */
	burnCentsPerHour: number;
	running: number;
}

export function fetchSpend(): Promise<{ spend: SpendReading[] }> {
	return get('/api/v1/admin/entry/spend');
}

/** Behind a step-up: a raised cap is a purchase the hub makes later by itself. */
export function saveSpendCaps(
	role: SpendRole,
	dailyCapCents: number,
	monthlyCapCents: number,
): Promise<{ spend: SpendReading[] }> {
	return put(`/api/v1/admin/entry/spend/${role}`, { dailyCapCents, monthlyCapCents });
}

/** What the canary judges a build by (migration 067). */
export interface BenchPolicy {
	/** the share of the old build's throughput the new one must keep, in percent */
	minThroughputPct: number;
	/** how much chat rtt p50 may grow, in percent of the old build's */
	maxRttGrowthPct: number;
	/** how many before/after pairs one verdict is averaged over */
	pairs: number;
	/** how long one run lasts */
	runSeconds: number;
	/** how long a canary will spend benching at this policy: pairs × 2 × run */
	benchingSeconds: number;
	updatedAt: string;
	updatedBy: string;
}

export function fetchBenchPolicy(): Promise<BenchPolicy> {
	return get('/api/v1/admin/entry/bench-policy');
}

/**
 * Turn one knob of the canary. Only the fields that moved are sent.
 *
 * Behind a step-up on the server: a looser throughput threshold lets a slower
 * build onto every entry point in the fleet.
 */
export function saveBenchPolicy(patch: {
	minThroughputPct?: number;
	maxRttGrowthPct?: number;
	pairs?: number;
	runSeconds?: number;
}): Promise<BenchPolicy> {
	return put('/api/v1/admin/entry/bench-policy', patch);
}

/** How long a stage took, in the console's words. Null is "нет данных". */
export function formatSeconds(sec: number | null): string {
	if (sec === null) return '—';
	if (sec < 1) return '<1 с';
	if (sec < 60) return `${Math.round(sec)} с`;
	if (sec < 3600) {
		const m = Math.floor(sec / 60);
		const s = Math.round(sec % 60);
		return s ? `${m} м ${s} с` : `${m} м`;
	}
	if (sec < 86_400) {
		const h = Math.floor(sec / 3600);
		const m = Math.round((sec % 3600) / 60);
		return m ? `${h} ч ${m} м` : `${h} ч`;
	}
	return `${(sec / 86_400).toFixed(1)} дн`;
}

/** What an ending is called on screen. */
export const RESOLUTION_LABEL: Record<IncidentResolution, string> = {
	auto: 'закрыл автопилот',
	handed: 'отдал тебе',
	failed: 'ремонт не удался',
	dismissed: 'отклонено',
	working: 'в работе',
};

/** Close an incident by hand: the cross on a queue card. */
export function dismissIncident(id: string, outcome = ''): Promise<{ ok: boolean }> {
	return post(`/api/v1/admin/entry/incidents/${encodeURIComponent(id)}/dismiss`, { outcome });
}

/** What was tried against this incident, oldest first. */
export function fetchIncidentRepairs(id: string): Promise<{ repairs: IncidentRepair[] }> {
	return get(`/api/v1/admin/entry/incidents/${encodeURIComponent(id)}/repairs`);
}

export function fetchOverview(window: OverviewWindow = '7d'): Promise<EntryOverview> {
	return get(`/api/v1/admin/entry/overview?window=${encodeURIComponent(window)}`);
}

// ── The protocols screen ───────────────────────────────────────────────────

/** One protocol or one machine: one value per bucket, null where nobody measured. */
export interface ProtocolSeries {
	key: string;
	proto: string;
	domain?: string;
	retired?: boolean;
	/** machines alive for a protocol, relays alive for a machine */
	live: (number | null)[];
	passPct: (number | null)[];
	trafficBytes: (number | null)[];
	conns: (number | null)[];
	subscribers: (number | null)[];
	costCents: (number | null)[];
}

export interface EntryProtocolStats {
	window: string;
	bucketSeconds: number;
	buckets: string[];
	protocols: ProtocolSeries[];
	nodes: ProtocolSeries[];
	missing: MissingFigure[];
}

export function fetchProtocolStats(window: OverviewWindow = '24h'): Promise<EntryProtocolStats> {
	return get(`/api/v1/admin/entry/protocols?window=${encodeURIComponent(window)}`);
}

/** What a protocol is called on screen; an unknown one keeps its own name. */
export const PROTO_LABEL: Record<string, string> = {
	'vless-reality-vision': 'vless-reality',
	'vless-xhttp': 'xhttp',
	hysteria2: 'hysteria2',
	direct: 'direct',
	unknown: 'прочие релеи',
};

/** What a protocol is for, said in the console's words. */
export const PROTO_ROLE: Record<string, string> = {
	'vless-reality-vision': 'основной',
	'vless-xhttp': 'запасной, на домене',
	hysteria2: 'голос',
	direct: 'без релея',
	unknown: 'снят с учёта',
};

/** What a cause is called on screen. */
export const CAUSE_LABEL: Record<string, string> = {
	healthy: 'машина жива',
	blind: 'пробник без сети',
	domain_blocked: 'домен заблокирован',
	fingerprint: 'протокол спалили',
	ports_banned: 'часть портов забанена',
	ip_banned: 'ip забанен целиком',
	unknown: 'причина не ясна',
	edge_down: 'релей не запущен',
	provider_down: 'машина не отвечает',
	destination: 'прикрытие мертво',
	short_of_ports: 'нет запасного порта',
	short_of_covers: 'неполный набор прикрытий',
	short_of_fleet: 'в пуле не хватает машин',
	short_of_domains: 'в пуле не хватает доменов',
};

/** What a playbook is called on screen. */
export const PLAYBOOK_LABEL: Record<string, string> = {
	evict_domain: 'снять домен, взять из пула',
	buy_domain: 'купить домен в пул',
	rotate_reality: 'сменить ключ reality',
	evict_box: 'снять машину с выдачи',
	retire_ports: 'снять порты с выдачи',
	reboot_box: 'перезагрузить машину',
	restart_edge: 'поднять юнит релея',
	rotate_dest: 'сменить прикрытие',
	add_ports: 'поднять запасной порт',
	add_covers: 'вернуть прикрытия',
	scale_up: 'добавить машину в пул',
};
