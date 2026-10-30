import { get, patch, post } from './client';

export type ReportStatus = 'new' | 'in_review' | 'resolved' | 'dismissed';

export interface UserReportRow {
	id: string;
	reporterId: string;
	reporterUsername: string;
	targetId: string;
	targetUsername: string;
	reason: string;
	context: string;
	category: 'harassment' | 'spam' | 'nsfw' | 'impersonation' | 'other';
	status: ReportStatus;
	reviewerId?: string;
	reviewerUsername?: string;
	reviewerNote: string;
	createdAt: string;
	resolvedAt?: string;
	userAgent?: string;
	targetKind: string;
	targetRef?: Record<string, any> | null;
	contentSnapshot: string;
}

export interface UserReportDetail {
	report: UserReportRow;
	targetReportCount30d: number;
	muteState?: { muted: boolean; expiresAt?: string };
}

export interface StreamReportRow {
	id: string;
	reporterId: string;
	reporterUsername: string;
	sharerId?: string;
	sharerUsername?: string;
	category: 'video' | 'audio' | 'lag' | 'content' | 'other';
	description: string;
	status: ReportStatus;
	reviewerId?: string;
	reviewerUsername?: string;
	reviewerNote: string;
	createdAt: string;
	resolvedAt?: string;
	userAgent?: string;
	targetKind: string;
	targetRef?: Record<string, any> | null;
	contentSnapshot: string;
}

export interface StreamReportDetail {
	report: StreamReportRow;
	sharerReportCount30d: number;
}

export interface PatchReportBody {
	status?: ReportStatus;
	reviewerNote?: string;
}

export interface GroupedUserReportRow {
	groupKey: string;
	targetId: string;
	targetUsername: string;
	targetKind: string;
	targetRef?: Record<string, any> | null;
	reportCount: number;
	reportIds: string[];
	categories: string[];
	statuses: ReportStatus[];
	previewReason: string;
	previewSnapshot: string;
	lastReportAt: string;
	firstReportAt: string;
}

export async function getUserReports(params: {
	status?: ReportStatus;
	category?: string;
	targetKind?: string;
	targetKindNot?: string;
	group?: 'target';
	limit?: number;
	offset?: number;
}): Promise<{ reports?: UserReportRow[]; groups?: GroupedUserReportRow[] }> {
	return get<{ reports?: UserReportRow[]; groups?: GroupedUserReportRow[] }>(
		'/api/v1/admin/reports/users',
		params,
	);
}

export async function getUserReport(id: string): Promise<UserReportDetail> {
	return get<UserReportDetail>(`/api/v1/admin/reports/users/${encodeURIComponent(id)}`);
}

export async function patchUserReport(id: string, body: PatchReportBody): Promise<{ status: string }> {
	return patch<{ status: string }>(`/api/v1/admin/reports/users/${encodeURIComponent(id)}`, body);
}

export async function getStreamReports(params: {
	status?: ReportStatus;
	category?: string;
	limit?: number;
	offset?: number;
}): Promise<{ reports: StreamReportRow[] }> {
	return get<{ reports: StreamReportRow[] }>('/api/v1/admin/reports/streams', params);
}

export async function getStreamReport(id: string): Promise<StreamReportDetail> {
	return get<StreamReportDetail>(`/api/v1/admin/reports/streams/${encodeURIComponent(id)}`);
}

export async function patchStreamReport(id: string, body: PatchReportBody): Promise<{ status: string }> {
	return patch<{ status: string }>(`/api/v1/admin/reports/streams/${encodeURIComponent(id)}`, body);
}

// Quick-action endpoints (see internal/admin/reports_actions.go). The
// action discriminator drives the server-side dispatch — params is reserved
// for future per-action options (durations, custom reasons).
export type ReportActionName =
	| 'delete_message'
	| 'delete_post'
	| 'ban_user_server'
	| 'ban_user_global'
	| 'mute_user_server'
	| 'unmute_user_server'
	| 'force_stop_stream';

export interface ReportActionBody {
	action: ReportActionName;
	params?: Record<string, unknown>;
}

export interface ReportActionResult {
	ok: boolean;
	action: ReportActionName;
	extra?: string;
}

export async function executeUserReportAction(id: string, body: ReportActionBody): Promise<ReportActionResult> {
	return post<ReportActionResult>(`/api/v1/admin/reports/users/${encodeURIComponent(id)}/action`, body);
}

export async function executeStreamReportAction(id: string, body: ReportActionBody): Promise<ReportActionResult> {
	return post<ReportActionResult>(`/api/v1/admin/reports/streams/${encodeURIComponent(id)}/action`, body);
}
