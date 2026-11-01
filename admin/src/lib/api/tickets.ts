import { del, get, patch, post } from './client';

export type TicketStatus = 'open' | 'analyzed' | 'resolved' | 'dismissed';
export type TicketType = 'bug' | 'idea';

export interface AdminTicketRow {
	id: number;
	type: TicketType;
	userId: string;
	username: string;
	displayName: string;
	tsEstimated: string;
	text: string;
	status: TicketStatus;
	aiSummary?: string;
	createdAt: string;
}

export interface AdminTicketDetail extends AdminTicketRow {
	snapshot: unknown;
}

export interface AdminTicketsResponse {
	tickets: AdminTicketRow[];
}

export interface PatchTicketBody {
	status?: TicketStatus;
	aiSummary?: string;
}

export async function fetchTickets(
	params: { status?: string; type?: string; limit?: number; offset?: number } = {},
): Promise<AdminTicketsResponse> {
	return get<AdminTicketsResponse>('/api/v1/admin/tickets', params as Record<string, string | number | undefined>);
}

export async function fetchTicket(id: number): Promise<AdminTicketDetail> {
	return get<AdminTicketDetail>(`/api/v1/admin/tickets/${id}`);
}

export async function patchTicket(id: number, body: PatchTicketBody): Promise<{ ok: boolean }> {
	return patch<{ ok: boolean }>(`/api/v1/admin/tickets/${id}`, body);
}

export async function deleteTicket(id: number): Promise<{ ok: boolean }> {
	return del<{ ok: boolean }>(`/api/v1/admin/tickets/${id}`);
}

export async function fetchAIContext(id: number): Promise<unknown> {
	return get<unknown>(`/api/v1/admin/tickets/${id}/ai-context`);
}

/** Answers the reporter in their chat with «Hikkari» (same as «Обращения»). */
export async function replyToTicket(id: number, text: string): Promise<{ ok: boolean }> {
	return post<{ ok: boolean }>(`/api/v1/admin/tickets/${id}/reply`, { text });
}

export interface AdminConvMessage {
	id: string;
	authorKind: 'admin' | 'system' | 'user';
	senderId?: string;
	senderUsername?: string;
	content: string;
	messageType?: string;
	/** Storage key, not a url — build one with `mediaUrl()`. */
	attachmentKey?: string;
	attachmentType?: string;
	fileName?: string;
	createdAt: string;
}

export interface AdminTicketConversation {
	conversationId: string;
	messages: AdminConvMessage[];
	/** The user's read marker in the support DM; null until they open it. */
	userReadAt?: string | null;
}

export async function fetchTicketConversation(id: number): Promise<AdminTicketConversation> {
	return get<AdminTicketConversation>(`/api/v1/admin/tickets/${id}/conversation`);
}

// Moves the official account's read marker in the ticket's support DM and
// records the read receipt, so the user's ticks flip. Called whenever the
// thread is read while the page is visible.
export async function markTicketRead(id: number): Promise<{ ok: boolean; conversationId?: string }> {
	return post<{ ok: boolean; conversationId?: string }>(`/api/v1/admin/tickets/${id}/mark-read`);
}
