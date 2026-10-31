import { get, post, put } from './client';

// Subscriptions, plans and orders (internal/billing/admin.go). All of it is
// the owner's; revoking asks for a passkey touch (client.ts steps up).

export type SubscriptionState = 'live' | 'expired' | 'spent' | 'revoked';

export interface Subscription {
	name: string;
	email: string;
	plan: string;
	state: SubscriptionState;
	createdAt: string;
	expiresAt: string | null;
	revokedAt: string | null;
	/** the period's allowance, 0 for none */
	trafficBytes: number;
	usedBytes: number;
	relays: string[];
}

export interface Plan {
	id: string;
	title: string;
	days: number;
	trafficGiB: number;
	/** kopecks */
	priceCents: number;
	retired: boolean;
	subscriptions: number;
}

export type OrderStatus = 'pending' | 'paid' | 'canceled' | 'expired';

export interface Order {
	id: string;
	plan: string;
	amountCents: number;
	email: string;
	renewal: boolean;
	/** the subscription's name; '' until a new subscription is paid for */
	account: string;
	provider: string;
	status: OrderStatus;
	createdAt: string;
	closedAt: string | null;
}

export interface Overview {
	/** '' while no payment provider is connected */
	provider: string;
	plans: Plan[];
	orders: Order[];
	totals: { paid30dCents: number; paid30dOrders: number; live: number };
}

export const fetchBillingOverview = (status = '') =>
	get<Overview>('/api/v1/admin/billing', { status: status || undefined });

export const fetchSubscriptions = (q = '') =>
	get<{ subscriptions: Subscription[] }>('/api/v1/admin/billing/subscriptions', { q: q || undefined }).then(
		(r) => r.subscriptions,
	);

export interface SubscriptionDetail {
	subscription: Subscription;
	/** '' for a revoked one or one issued before links were kept */
	url: string;
	orders: Order[];
}

export const fetchSubscription = (name: string) =>
	get<SubscriptionDetail>(`/api/v1/admin/billing/subscriptions/${encodeURIComponent(name)}`);

export const issueSubscription = (name: string, plan: string) =>
	post<{ url: string }>('/api/v1/admin/billing/subscriptions', { name, plan });

export const renewSubscription = (name: string, plan: string) =>
	post<{ subscription: Subscription }>(`/api/v1/admin/billing/subscriptions/${encodeURIComponent(name)}/renew`, {
		plan,
	});

export const revokeSubscription = (name: string) =>
	post<{ subscription: Subscription }>(`/api/v1/admin/billing/subscriptions/${encodeURIComponent(name)}/revoke`);

export const savePlan = (p: Pick<Plan, 'id' | 'title' | 'days' | 'trafficGiB' | 'priceCents'>) =>
	put<{ id: string }>(`/api/v1/admin/billing/plans/${encodeURIComponent(p.id)}`, {
		title: p.title,
		days: p.days,
		trafficGiB: p.trafficGiB,
		priceCents: p.priceCents,
	});

export const retirePlan = (id: string) =>
	post<{ id: string }>(`/api/v1/admin/billing/plans/${encodeURIComponent(id)}/retire`);
