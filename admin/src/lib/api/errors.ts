import { get } from './client';

export interface TopErrorRow {
	fingerprint: string;
	sampleMsg: string;
	sampleCat: string;
	sampleFile: string;
	sampleLine: number;
	sampleStack?: string;
	errors: number;
	warns: number;
	usersAffected: number;
	lastSeen: string;
}

export interface TopErrorsResponse {
	rows: TopErrorRow[];
}

export interface TopErrorsParams {
	from?: string;
	to?: string;
	min_count?: number;
	limit?: number;
	order?: 'count' | 'last_seen' | 'users';
}

export function fetchTopErrors(params: TopErrorsParams = {}): Promise<TopErrorsResponse> {
	return get<TopErrorsResponse>('/api/v1/admin/errors', params as Record<string, string | number | undefined>);
}
