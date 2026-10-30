// The support inbox — every conversation users have with the official
// account «Nekko», whether or not a ticket stands behind it. Tickets remain in
// tickets.ts; their milestones land in these same conversations.
import { get, post } from './client';
import type { AdminConvMessage, TicketStatus } from './tickets';

/** One обращение: a question opened by a plain message, or a bug report.
 *  Its status moves through `patchTicket` (tickets.ts), which also posts the
 *  milestone into the user's chat. */
export interface SupportCase {
	/** Row id — what `patchTicket` takes. */
	id: number;
	/** `case_no`: the random number the user reads («обращение №N»). */
	number: number;
	type: 'question' | 'bug';
	status: TicketStatus;
	text: string;
	createdAt: string;
}

export interface SupportInboxRow {
	conversationId: string;
	userId: string;
	username: string;
	displayName: string;
	lastText: string;
	lastAuthorKind: 'admin' | 'system' | 'user';
	lastAt: string;
	/** User messages after the team's read marker. */
	unread: number;
	/** The user's cases still open or in work. */
	openTickets: number;
	/** The current case: the active one, else the latest. Absent until the user asks something. */
	case?: SupportCase;
}

export interface SupportUser {
	id: string;
	username: string;
	displayName: string;
}

export interface SupportConversation {
	conversationId: string;
	user: SupportUser;
	messages: AdminConvMessage[];
	userReadAt?: string | null;
	/** Active first, then newest. */
	cases: SupportCase[];
}

export async function fetchSupportInbox(
	params: { unread?: boolean; limit?: number; offset?: number } = {},
): Promise<{ conversations: SupportInboxRow[] }> {
	return get<{ conversations: SupportInboxRow[] }>('/api/v1/admin/support/conversations', {
		unread: params.unread ? '1' : undefined,
		limit: params.limit,
		offset: params.offset,
	});
}

export async function fetchSupportConversation(convId: string): Promise<SupportConversation> {
	return get<SupportConversation>(`/api/v1/admin/support/conversations/${encodeURIComponent(convId)}`);
}

export async function replySupportConversation(convId: string, text: string): Promise<{ ok: boolean }> {
	return post<{ ok: boolean }>(`/api/v1/admin/support/conversations/${encodeURIComponent(convId)}/reply`, { text });
}

export async function markSupportConversationRead(convId: string): Promise<{ ok: boolean }> {
	return post<{ ok: boolean }>(`/api/v1/admin/support/conversations/${encodeURIComponent(convId)}/mark-read`);
}
