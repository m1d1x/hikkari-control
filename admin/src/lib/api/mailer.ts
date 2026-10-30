import { get, post, patch } from './client';
import type {
	MailerTemplatesResponse,
	MailerConfig,
	SendTestRequest,
	SendTestResponse,
	PreviewRequest,
	PreviewResponse,
	MailerLogResponse,
} from '$lib/types/mailer';

// Mailer endpoints have no mock — they're admin-only test infrastructure
// and there's no useful UI state to render without the real upstream.

export function getMailerTemplates(): Promise<MailerTemplatesResponse> {
	return get<MailerTemplatesResponse>('/api/v1/admin/mailer/templates');
}

export function getMailerConfig(): Promise<MailerConfig> {
	return get<MailerConfig>('/api/v1/admin/mailer/config');
}

// Flip the active mailer provider. Persists to mailer_settings on the
// upstream node so the choice survives a restart. Throws on 400/502 from
// the API; the badge UI shows the error inline.
export function setMailerProvider(
	provider: 'smtp' | 'unisender',
): Promise<{ provider: string }> {
	return patch<{ provider: string }>('/api/v1/admin/mailer/config', { provider });
}

export function previewTemplate(req: PreviewRequest): Promise<PreviewResponse> {
	return post<PreviewResponse>('/api/v1/admin/mailer/preview', req);
}

export function sendTestEmail(req: SendTestRequest): Promise<SendTestResponse> {
	return post<SendTestResponse>('/api/v1/admin/mailer/test', req);
}

export interface GetMailerLogParams {
	limit?: number;
	offset?: number;
	recipient?: string;
	status?: 'sent' | 'failed';
}

export function getMailerLog(params: GetMailerLogParams = {}): Promise<MailerLogResponse> {
	return get<MailerLogResponse>('/api/v1/admin/mailer/log', {
		limit: params.limit,
		offset: params.offset,
		recipient: params.recipient,
		status: params.status,
	});
}
