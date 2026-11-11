import { get, post, put } from './client';
import { USE_MOCK } from '$lib/config/mock';
import { mockBillingOverview, mockFleetNodes, mockSubscription, mockSubscriptions } from './billingMock';

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
	/** the account holds the whole fleet and the client turns tester mode on */
	tester: boolean;
	/** what the client shows for the subscription; '' for «hikkari» */
	title: string;
	/** the fleet picked by hand, node slugs; empty for the pool's batch */
	nodes: string[];
}

/** A logical node of the fleet, what a subscription's fleet is picked from. */
export interface FleetNode {
	slug: string;
	label: string;
	proto: string;
	region: string;
	box: string;
	/** a serving relay stands behind it now */
	alive: boolean;
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

export const fetchBillingOverview = (status = ''): Promise<Overview> =>
	USE_MOCK ? Promise.resolve(mockBillingOverview(status)) : get<Overview>('/api/v1/admin/billing', { status: status || undefined });

export const fetchSubscriptions = (q = ''): Promise<Subscription[]> =>
	USE_MOCK ? Promise.resolve(mockSubscriptions(q)) : get<{ subscriptions: Subscription[] }>('/api/v1/admin/billing/subscriptions', { q: q || undefined }).then(
		(r) => r.subscriptions,
	);

export interface SubscriptionDetail {
	subscription: Subscription;
	/** '' for a revoked one or one issued before links were kept */
	url: string;
	orders: Order[];
}

export const fetchSubscription = (name: string): Promise<SubscriptionDetail> =>
	USE_MOCK ? Promise.resolve(mockSubscription(name)) : get<SubscriptionDetail>(`/api/v1/admin/billing/subscriptions/${encodeURIComponent(name)}`);

export interface IssueInput {
	/** account name, [a-z0-9_-]; '' lets the hub pick sub-xxxxxx or tst-xxxxxx */
	name: string;
	plan: string;
	/** a tester's link: the whole fleet, and the client turns tester mode on */
	tester: boolean;
	title: string;
	/** node slugs; empty for the pool's batch */
	nodes: string[];
}

export const issueSubscription = (input: IssueInput) =>
	post<{ name: string; url: string }>('/api/v1/admin/billing/subscriptions', input);

export const setSubscriptionTitle = (name: string, title: string) =>
	put<{ subscription: Subscription }>(`/api/v1/admin/billing/subscriptions/${encodeURIComponent(name)}/title`, { title });

/** Works on a subscription already handed out: the client gets the new links on its next fetch. */
export const setSubscriptionFleet = (name: string, nodes: string[]) =>
	put<{ subscription: Subscription }>(`/api/v1/admin/billing/subscriptions/${encodeURIComponent(name)}/fleet`, { nodes });

export const fetchFleetNodes = (): Promise<FleetNode[]> =>
	USE_MOCK
		? Promise.resolve(mockFleetNodes)
		: get<{ nodes: FleetNode[] }>('/api/v1/admin/billing/nodes').then((r) => r.nodes);

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
