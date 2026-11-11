import type { FleetNode, Order, Overview, Plan, Subscription, SubscriptionDetail } from './billing';
import type { TesterInstall, TesterRelayScreen } from './support';

// Fixture for `VITE_USE_MOCK=true`: the subscriptions screen with plans,
// orders, subscriptions in every state and a few testers. Deterministic.

const GIB = 2 ** 30;
const ago = (days: number, hours = 0) => new Date(Date.now() - (days * 24 + hours) * 3600_000).toISOString();
const ahead = (days: number) => new Date(Date.now() + days * 86400_000).toISOString();

const plans: Plan[] = [
	{ id: 'month', title: 'Месяц', days: 30, trafficGiB: 300, priceCents: 29900, retired: false, subscriptions: 4 },
	{ id: 'quarter', title: '3 месяца', days: 90, trafficGiB: 1000, priceCents: 69900, retired: false, subscriptions: 2 },
	{ id: 'year', title: 'Год', days: 365, trafficGiB: 4000, priceCents: 199000, retired: false, subscriptions: 1 },
	{ id: 'trial', title: 'Пробный', days: 7, trafficGiB: 20, priceCents: 0, retired: true, subscriptions: 3 },
];

const subs: Subscription[] = [
	{ name: 'anna-k', email: 'anna.k@example.com', plan: 'month', state: 'live', createdAt: ago(12), expiresAt: ahead(18), revokedAt: null, trafficBytes: 300 * GIB, usedBytes: 96 * GIB, relays: ['ams7', 'fra2'], tester: false, title: '', nodes: [] },
	{ name: 'denis-m', email: 'denis@example.com', plan: 'quarter', state: 'live', createdAt: ago(41), expiresAt: ahead(49), revokedAt: null, trafficBytes: 1000 * GIB, usedBytes: 412 * GIB, relays: ['ams7', 'hel1', 'fra2'], tester: false, title: '', nodes: [] },
	{ name: 'olga-s', email: 'olga.s@example.com', plan: 'year', state: 'live', createdAt: ago(130), expiresAt: ahead(235), revokedAt: null, trafficBytes: 4000 * GIB, usedBytes: 1210 * GIB, relays: ['ams7'], tester: false, title: '', nodes: [] },
	{ name: 'igor-v', email: 'igor.v@example.com', plan: 'month', state: 'spent', createdAt: ago(20), expiresAt: ahead(10), revokedAt: null, trafficBytes: 300 * GIB, usedBytes: 300 * GIB, relays: ['fra2'], tester: false, title: '', nodes: [] },
	{ name: 'maria-t', email: 'maria.t@example.com', plan: 'month', state: 'expired', createdAt: ago(63), expiresAt: ago(33), revokedAt: null, trafficBytes: 300 * GIB, usedBytes: 188 * GIB, relays: ['hel1'], tester: false, title: '', nodes: [] },
	{ name: 'pavel-r', email: 'pavel@example.com', plan: 'quarter', state: 'revoked', createdAt: ago(55), expiresAt: ahead(35), revokedAt: ago(6), trafficBytes: 1000 * GIB, usedBytes: 57 * GIB, relays: [], tester: false, title: '', nodes: [] },
	{ name: 'tst-9f31ab', email: '', plan: 'month', state: 'live', createdAt: ago(25), expiresAt: ahead(5), revokedAt: null, trafficBytes: 0, usedBytes: 74 * GIB, relays: ['ams7', 'ams3', 'fra2', 'hel1', 'euw1'], tester: true, title: 'тест Пети', nodes: ['n-ams7', 'n-fra2'] },
];

const orders: Order[] = [
	{ id: 'ord-1041', plan: 'month', amountCents: 29900, email: 'anna.k@example.com', renewal: false, account: 'anna-k', provider: 'yookassa', status: 'paid', createdAt: ago(12), closedAt: ago(12) },
	{ id: 'ord-1040', plan: 'year', amountCents: 199000, email: 'olga.s@example.com', renewal: false, account: 'olga-s', provider: 'yookassa', status: 'paid', createdAt: ago(130), closedAt: ago(130) },
	{ id: 'ord-1039', plan: 'quarter', amountCents: 69900, email: 'denis@example.com', renewal: true, account: 'denis-m', provider: 'yookassa', status: 'paid', createdAt: ago(41), closedAt: ago(41) },
	{ id: 'ord-1038', plan: 'month', amountCents: 29900, email: 'new@example.com', renewal: false, account: '', provider: 'yookassa', status: 'pending', createdAt: ago(0, 2), closedAt: null },
	{ id: 'ord-1037', plan: 'month', amountCents: 29900, email: 'igor.v@example.com', renewal: false, account: 'igor-v', provider: 'yookassa', status: 'paid', createdAt: ago(20), closedAt: ago(20) },
	{ id: 'ord-1036', plan: 'quarter', amountCents: 69900, email: 'x@example.com', renewal: false, account: '', provider: 'yookassa', status: 'canceled', createdAt: ago(9), closedAt: ago(9) },
	{ id: 'ord-1035', plan: 'month', amountCents: 29900, email: 'y@example.com', renewal: false, account: '', provider: 'yookassa', status: 'expired', createdAt: ago(15), closedAt: ago(14) },
];

export function mockBillingOverview(status: string): Overview {
	const paid = orders.filter((o) => o.status === 'paid' && Date.now() - new Date(o.createdAt).getTime() < 30 * 86400_000);
	return {
		provider: 'yookassa',
		plans,
		orders: status ? orders.filter((o) => o.status === status) : orders,
		totals: { paid30dCents: paid.reduce((a, o) => a + o.amountCents, 0), paid30dOrders: paid.length, live: subs.filter((s) => s.state === 'live').length },
	};
}

export function mockSubscriptions(q: string): Subscription[] {
	const needle = q.toLowerCase();
	return needle ? subs.filter((s) => s.name.includes(needle) || s.email.includes(needle)) : subs;
}

export function mockSubscription(name: string): SubscriptionDetail {
	const subscription = subs.find((s) => s.name === name) ?? subs[0];
	return {
		subscription,
		url: subscription.state === 'revoked' ? '' : `https://sub.hikkari.example/s/${subscription.name}`,
		orders: orders.filter((o) => o.account === subscription.name),
	};
}

export const mockTesterInstalls: { installs: TesterInstall[] } = {
	installs: [
		{ alias: 'tester-001', install_id: 't-3a9c71d2', note: 'Windows, Москва', online: true, platform: 'windows', version: '1.0.58', chunks: 412, lines: 38120, dropped: 0, first_seen: ago(40), last_seen: ago(0, 0.02) },
		{ alias: 'tester-002', install_id: 't-cbfed41c', note: 'Mac, СПб', online: true, platform: 'macos', version: '1.0.58', chunks: 301, lines: 22780, dropped: 14, first_seen: ago(33), last_seen: ago(0, 0.05) },
		{ alias: 'tester-003', install_id: 't-71e0b9a4', note: 'Android', online: false, platform: 'android', version: '1.0.55', chunks: 88, lines: 6120, dropped: 0, first_seen: ago(18), last_seen: ago(2) },
	],
};

export const mockTesterRelays: TesterRelayScreen = {
	window: '24h',
	bucketSeconds: 3600,
	buckets: [],
	protocols: [],
	clientsByUp: [],
	failures: [],
	installs: [
		{ install_id: 't-3a9c71d2', platform: 'windows', version: '1.0.58', at: ago(0, 0.1), relays: [
			{ tag: 'ams7-443', proto: 'vless-reality-vision', up: true, pingMs: 61 },
			{ tag: 'fra2-443', proto: 'vless-xhttp', up: true, pingMs: 74 },
			{ tag: 'hel1-8443', proto: 'hysteria2', up: false, reason: 'timeout' },
		] },
		{ install_id: 't-cbfed41c', platform: 'macos', version: '1.0.58', at: ago(0, 0.1), relays: [
			{ tag: 'ams7-443', proto: 'vless-reality-vision', up: true, pingMs: 83 },
			{ tag: 'fra2-443', proto: 'vless-xhttp', up: false, reason: 'timeout' },
		] },
		{ install_id: 't-71e0b9a4', platform: 'android', version: '1.0.55', at: ago(2), relays: [
			{ tag: 'ams7-443', proto: 'vless-reality-vision', up: true, pingMs: 120 },
		] },
	],
};

export const mockFleetNodes: FleetNode[] = [
	{ slug: 'n-ams7', label: 'нидерланды-101', proto: 'vless-reality-vision', region: '', box: 'ams7', alive: true },
	{ slug: 'n-ams3', label: 'нидерланды-102', proto: 'hikkari', region: '', box: 'ams3', alive: true },
	{ slug: 'n-fra2', label: 'германия-101', proto: 'vless-xhttp', region: '', box: 'fra2', alive: true },
	{ slug: 'n-hel1', label: 'финляндия-101', proto: 'hysteria2', region: 'eu', box: 'hel1', alive: true },
	{ slug: 'n-euw1', label: 'франция-101', proto: 'naive', region: '', box: 'euw1', alive: false },
];
