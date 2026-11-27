import { USE_MOCK } from '$lib/config/mock';
import { get, patch, put } from './client';

// Hoster accounts and manual billing (internal/admin/machines_billing.go).
// The fleet API knows what a machine is and what it cost per month; this one
// knows where you pay for it, under what account, and how much traffic its
// plan includes. Manual purchases have no API at the hoster at all, so these
// numbers are typed by the owner and live in the hub's database.

export interface ProviderAccount {
	/** hoster kind: aeza, u1, cloud4box, ihostart, … */
	provider: string;
	displayName: string;
	/** origin AS of the hoster's network, free text ('AS203273') */
	asn: string;
	/** where the billing console is, opened from the hoster's name */
	billingUrl: string;
	apiHost: string;
	spendCapCents?: number | null;
	note: string;
	hasLogin: boolean;
	hasPassword: boolean;
	hasApiKey: boolean;
	/** machines of this hoster in service */
	machines: number;
	/** their rent, in cents, summed */
	monthlyCents: number;
	/** '' when the machines carry different currencies */
	currency: string;
}

export interface ProviderAccounts {
	accounts: ProviderAccount[];
}

export type SecretField = 'login' | 'password' | 'apiKey';

export interface ProviderPatch {
	displayName?: string;
	asn?: string;
	billingUrl?: string;
	apiHost?: string;
	spendCapCents?: number | null;
	note?: string;
}

/** What the owner types into one machine's billing cells. */
export interface BoxBillingPatch {
	priceAmount?: number | null;
	priceCurrency?: string;
	trafficQuotaGb?: number | null;
	/** YYYY-MM-DD */
	rentedUntil?: string | null;
	plan?: string;
}

export async function fetchProviderAccounts(): Promise<ProviderAccounts> {
	if (USE_MOCK) return mockAccounts();
	return get<ProviderAccounts>('/api/v1/admin/entry/provider-accounts');
}

export function saveProvider(kind: string, body: ProviderPatch): Promise<ProviderAccount> {
	if (USE_MOCK) return Promise.resolve({ ...mockAccounts().accounts[0], provider: kind });
	return patch<{ account: ProviderAccount }>(
		`/api/v1/admin/entry/provider-accounts/${encodeURIComponent(kind)}`,
		body,
	).then((r) => r.account);
}

export function saveProviderSecret(
	kind: string,
	body: { login?: string; password?: string; apiKey?: string },
): Promise<{ hasLogin: boolean; hasPassword: boolean; hasApiKey: boolean }> {
	if (USE_MOCK) return Promise.resolve({ hasLogin: body.login !== '', hasPassword: body.password !== '', hasApiKey: body.apiKey !== '' });
	return put(`/api/v1/admin/entry/provider-accounts/${encodeURIComponent(kind)}/secret`, body);
}

/** Opens one secret on request; the hub writes this into the admin audit log. */
export async function readProviderSecret(kind: string, field: SecretField): Promise<string> {
	if (USE_MOCK) return field === 'login' ? 'owner@hikkari' : 'mock-secret';
	const res = await get<{ value: string }>(
		`/api/v1/admin/entry/provider-accounts/${encodeURIComponent(kind)}/secret/${field}`,
	);
	return res.value;
}

export function saveBoxBilling(name: string, body: BoxBillingPatch): Promise<unknown> {
	if (USE_MOCK) return Promise.resolve({ name, ...body });
	return patch(`/api/v1/admin/entry/machines/${encodeURIComponent(name)}/billing`, body);
}

function mockAccounts(): ProviderAccounts {
	return {
		accounts: [
			{
				provider: 'aeza',
				displayName: 'Aeza',
				asn: 'AS203273',
				billingUrl: 'https://my.aeza.net',
				apiHost: '',
				note: '',
				hasLogin: true,
				hasPassword: true,
				hasApiKey: false,
				machines: 2,
				monthlyCents: 2587,
				currency: 'EUR',
			},
			{
				provider: 'u1',
				displayName: 'U1',
				asn: 'AS213877',
				billingUrl: 'https://u1host.com',
				apiHost: '',
				note: 'продлевать на 3 месяца',
				hasLogin: true,
				hasPassword: false,
				hasApiKey: false,
				machines: 2,
				monthlyCents: 1130,
				currency: 'EUR',
			},
			{
				provider: 'ihostart',
				displayName: 'ihostart',
				asn: 'AS214209',
				billingUrl: 'https://panel.ihostart.com',
				apiHost: '',
				note: '',
				hasLogin: false,
				hasPassword: false,
				hasApiKey: false,
				machines: 1,
				monthlyCents: 1069,
				currency: 'EUR',
			},
		],
	};
}
