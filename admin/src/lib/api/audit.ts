import { get } from './client';

// AuditRow mirrors internal/admin/audit.go auditRow. Payload is left as
// `unknown` because it is action-specific JSON — UI either renders the raw
// blob or matches on action prefix to interpret the shape.
export interface AuditRow {
	id: number;
	ts: string;
	adminId: string;
	adminUsername: string;
	action: string;
	targetType: string;
	targetId: string;
	payload: Record<string, unknown> | null;
	ip: string;
	userAgent: string;
}

export type FetchAuditParams = {
	limit?: number;
	offset?: number;
	action?: string; // LIKE prefix, e.g. "report.action."
	adminId?: string; // exact UUID
	targetType?: string; // user_report / stream_report / user / message / post
	from?: string; // RFC3339
	to?: string; // RFC3339
	search?: string; // free-text on action / targetId / admin username
};

export async function fetchAudit(params: FetchAuditParams): Promise<{ entries: AuditRow[] }> {
	return get<{ entries: AuditRow[] }>('/api/v1/admin/audit', params);
}
