import { writable } from 'svelte/store';

export type TimeRange = '1h' | '24h' | '7d' | '30d' | '90d';

export const timeRange = writable<TimeRange>('7d');

export const timeRangeLabels: Record<TimeRange, string> = {
	'1h': '1 hour',
	'24h': '24 hours',
	'7d': '7 days',
	'30d': '30 days',
	'90d': '90 days',
};
