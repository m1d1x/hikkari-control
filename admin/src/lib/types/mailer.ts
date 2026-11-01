// MailerTemplate describes one transactional template registered on the
// upstream server. The admin UI uses `required` to render a dynamic
// set of variable inputs once the operator picks a template.
export interface MailerTemplate {
	id: string;
	required: string[];
}

export interface MailerTemplatesResponse {
	templates: MailerTemplate[];
	enabled: boolean;
}

// Active mailer backend snapshot returned by GET /api/v1/admin/mailer/config.
// `provider` is what's used at send time; `*_configured` flags say which
// backends could be flipped to via MAILER_PROVIDER without further env changes.
export interface MailerConfig {
	provider: 'smtp' | 'unisender';
	from_address: string;
	from_name: string;
	smtp_configured: boolean;
	unisender_configured: boolean;
}

// Two send modes share one endpoint — `template` selects the path. When
// `template` is set the request hits SendTransactional; otherwise it falls
// through to the raw send path which requires `subject`+`text_body`.
export interface SendTestRequest {
	to: string;
	to_name?: string;
	template?: string;
	variables?: Record<string, string>;
	subject?: string;
	text_body?: string;
	html_body?: string;
}

export interface SendTestResponse {
	status: 'sent';
	mode: 'template' | 'raw';
	template?: string;
	sent_at: string;
	message_id?: string;
	provider?: 'smtp' | 'unisender';
}

// Preview request mirrors SendTestRequest minus recipient — we don't
// actually send. Same body fields drive the iframe preview.
export interface PreviewRequest {
	template?: string;
	variables?: Record<string, string>;
	subject?: string;
	text_body?: string;
	html_body?: string;
}

export interface PreviewResponse {
	subject: string;
	text_body: string;
	html_body: string;
}

// One row of email_send_log returned by GET /mailer/log. Optional fields
// arrive null/undefined from the server; the table renders «—» for those.
export interface MailerLogRow {
	id: string;
	sent_at: string;
	sent_by_admin_id?: string | null;
	sent_by_admin_name?: string | null;
	recipient: string;
	template_id?: string | null;
	mode: 'template' | 'raw';
	subject: string;
	status: 'sent' | 'failed';
	error_msg?: string | null;
	upstream_message_id?: string | null;
	provider: 'smtp' | 'unisender';
	// Delivery status from Unisender Go webhook callbacks. Null for SMTP
	// sends (Selectel doesn't push status callbacks) and for older rows
	// that pre-date the webhook handler.
	last_status?: string | null;
	last_event_at?: string | null;
	delivered_at?: string | null;
	opened_at?: string | null;
	clicked_at?: string | null;
	bounced_at?: string | null;
}

export interface MailerLogResponse {
	items: MailerLogRow[];
	limit: number;
}
