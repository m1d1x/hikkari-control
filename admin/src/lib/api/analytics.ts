import { get, post, del } from './client';
import * as mock from '$lib/mock/analytics';
import { USE_MOCK } from '$lib/config/mock';
import type {
	OverviewMetrics,
	AnalyticsEvent,
	EventTypeOption,
	FunnelStep,
	UserProfile,
	ServerAnalytics,
	RealtimeStats,
	RetentionCohort,
	PeakHoursData,
	UXHealthData,
	ClientPerfData,
	SessionsData,
	OnboardingData,
	TrafficData,
} from '$lib/types/analytics';
import type { PaginatedResponse } from '$lib/types/common';

export const getOverviewMetrics = USE_MOCK
	? mock.getOverviewMetrics
	: async (): Promise<OverviewMetrics> => {
			const raw = await get<any>('/api/v1/admin/analytics/overview');
			// Backend returns flat { dau, wau, mau, totalUsers, ... }
			// Frontend expects nested { dau: {value, previousValue}, dauTimeSeries, ... }
			return {
				dau: { value: raw.dau ?? 0, previousValue: 0 },
				wau: { value: raw.wau ?? 0, previousValue: 0 },
				mau: { value: raw.mau ?? 0, previousValue: 0 },
				stickiness: { value: raw.stickiness ?? 0, previousValue: 0 },
				newUsers: { value: raw.newUsers ?? 0, previousValue: 0 },
				totalEvents: { value: raw.totalEvents ?? 0, previousValue: 0 },
				avgSessionsPerUser: { value: raw.avgSessionsPerUser ?? 0, previousValue: 0 },
				discoverRate: raw.discoverRate ?? 0,
				dauTimeSeries: raw.dauTimeSeries ?? { label: 'DAU', data: [] },
				wauTimeSeries: raw.wauTimeSeries ?? { label: 'WAU', data: [] },
				mauTimeSeries: raw.mauTimeSeries ?? { label: 'MAU', data: [] },
				newUsersTimeSeries: raw.newUsersTimeSeries ?? { label: 'New Users', data: [] },
				discoverRateTimeSeries: raw.discoverRateTimeSeries ?? { label: 'Discover Rate', data: [] },
				stickinessTimeSeries: raw.stickinessTimeSeries ?? { label: 'Stickiness', data: [] },
				eventsByType: raw.eventsByType ?? [],
				settingsByTab: raw.settingsByTab ?? [],
			};
		};

export const getEventTypes = USE_MOCK
	? mock.getEventTypes
	: async (): Promise<EventTypeOption[]> => {
			const res = await get<any[]>('/api/v1/admin/analytics/event-types');
			return (res || []).map((e: any) => ({
				value: e.eventType || e,
				label: (e.eventType || e).replace(/_/g, ' '),
				count: e.count || 0,
			}));
		};

export const getEvents = USE_MOCK
	? mock.getEvents
	: async (page: number, pageSize: number, eventType?: string): Promise<PaginatedResponse<AnalyticsEvent>> => {
			const offset = (page - 1) * pageSize;
			const res = await get<any>('/api/v1/admin/analytics/events', {
				limit: pageSize,
				offset,
				type: eventType,
			});
			return {
				data: res.events || res.data || [],
				total: res.total || 0,
				page,
				pageSize,
			};
		};

export const getFunnelSteps = USE_MOCK
	? mock.getFunnelSteps
	: (labels: string[]) =>
			get<FunnelStep[]>('/api/v1/admin/analytics/funnels', {
				labels: labels.join(','),
			});

/**
 * Username and id are separate filters on purpose: one is a fuzzy substring
 * match, the other a prefix of a UUID. Sorting is server-side and limited to
 * the columns PostgreSQL owns — the ClickHouse-derived activity numbers are
 * computed per page, so ordering by them would rank twenty rows, not the table.
 */
export interface UserListQuery {
	q?: string;
	id?: string;
	sort?: string;
	dir?: 'asc' | 'desc';
}

export const getUsers = USE_MOCK
	? mock.getUsers
	: async (page: number, pageSize: number, query: UserListQuery = {}): Promise<PaginatedResponse<any>> => {
			const offset = (page - 1) * pageSize;
			const res = await get<any>('/api/v1/admin/analytics/users', {
				limit: pageSize,
				offset,
				q: query.q || undefined,
				id: query.id || undefined,
				sort: query.sort || undefined,
				dir: query.dir || undefined,
			});
			return {
				data: res.users || res.data || [],
				total: res.total || 0,
				page,
				pageSize,
			};
		};

export const getUserProfile = USE_MOCK
	? mock.getUserProfile
	: async (userId: string): Promise<UserProfile> => {
			const raw = await get<any>(`/api/v1/admin/analytics/users/${userId}`);
			return {
				id: raw.user?.id ?? userId,
				username: raw.user?.username ?? '',
				createdAt: raw.user?.createdAt ?? '',
				lastSeen: raw.user?.lastSeen ?? '',
				totalMessages: raw.eventCount ?? 0,
				totalSessions: raw.sessions?.length ?? 0,
				serverCount: raw.servers?.length ?? 0,
				servers: (raw.servers ?? []).map((s: any) => ({
					id: s.serverId ?? s.id ?? '',
					name: s.serverName ?? s.name ?? '',
					joinedAt: s.joinedAt ?? '',
					messageCount: s.messageCount ?? 0,
					engagementWeight: s.engagementWeight ?? 0,
				})),
				sessions: raw.sessions ?? [],
				recentEvents: [],
				riskScore: raw.user?.riskScore ?? null,
				needPhoneVerification: raw.user?.needPhoneVerification ?? false,
				isShadowbanned: raw.user?.isShadowbanned ?? false,
				scoringLog: raw.scoringLog ?? [],
			};
		};

export const getServers = USE_MOCK
	? mock.getServers
	: async (page: number, pageSize: number): Promise<PaginatedResponse<any>> => {
			const offset = (page - 1) * pageSize;
			const res = await get<any>('/api/v1/admin/analytics/servers', {
				limit: pageSize,
				offset,
			});
			return {
				data: res.servers || res.data || [],
				total: res.total || 0,
				page,
				pageSize,
			};
		};

export const getServerAnalytics = USE_MOCK
	? mock.getServerAnalytics
	: (serverId: string) => get<ServerAnalytics>(`/api/v1/admin/analytics/servers/${serverId}`);

export const getRealtimeStats = USE_MOCK
	? mock.getRealtimeStats
	: async (): Promise<RealtimeStats> => {
			const raw = await get<any>('/api/v1/admin/analytics/realtime');
			return {
				onlineUsers: raw.onlineUsers ?? 0,
				inVoiceUsers: raw.inVoiceUsers ?? 0,
				eventsPerSecond: raw.eventsPerSecond ?? 0,
				activeDiscoverSessions: raw.activeDiscoverSessions ?? 0,
				recentJoins: raw.recentJoins ?? [],
				eventRateHistory: raw.eventRateHistory ?? [],
				onlineHistory: raw.onlineHistory ?? [],
				voiceHistory: raw.voiceHistory ?? [],
				eventsPerSecondHistory: raw.eventsPerSecondHistory ?? [],
				uxFrustrationHistory: raw.uxFrustrationHistory,
				uxCurrent: raw.uxCurrent,
			};
		};

export const getRetentionCohorts = USE_MOCK
	? mock.getRetentionCohorts
	: async (): Promise<RetentionCohort[]> => {
			const raw = await get<any[]>('/api/v1/admin/analytics/retention');
			if (!raw || raw.length === 0) return [];
			// Backend may return {cohort, size, retention[]} → adapt to {cohortWeek, size, weeks[]}
			return raw.map((r: any) => ({
				cohortWeek: r.cohortWeek || r.cohort || '',
				size: r.size || 0,
				weeks: r.weeks || r.retention || [],
			}));
		};

export const getPeakHoursData = USE_MOCK
	? mock.getPeakHoursData
	: async (): Promise<PeakHoursData[]> => {
			const raw = await get<any[]>('/api/v1/admin/analytics/peak-hours');
			if (!raw || raw.length === 0) return [];
			return raw.map((r: any) => ({
				day: r.day ?? 0,
				hour: r.hour ?? 0,
				value: r.value ?? r.count ?? 0,
			}));
		};

export const getUXHealth = USE_MOCK
	? mock.getUXHealth
	: async (): Promise<UXHealthData> => {
			const raw = await get<any>('/api/v1/admin/analytics/ux-health');
			return {
				rageClicks: raw.rageClicks ?? [],
				deadClicks: raw.deadClicks ?? [],
				quickBacks: raw.quickBacks ?? [],
				timeSeries: raw.timeSeries ?? [],
				totalRage: raw.totalRage ?? 0,
				totalDead: raw.totalDead ?? 0,
				totalQuick: raw.totalQuick ?? 0,
			};
		};

export const getClientPerf = async (): Promise<ClientPerfData> =>
	get<ClientPerfData>('/api/v1/admin/analytics/client-perf');

export const getSessionsData = async (): Promise<SessionsData> => get<SessionsData>('/api/v1/admin/analytics/sessions');

export const getOnboardingData = async (): Promise<OnboardingData> =>
	get<OnboardingData>('/api/v1/admin/analytics/onboarding');

export const getTrafficData = USE_MOCK
	? mock.getTrafficData
	: (period = '7d'): Promise<TrafficData> => get<TrafficData>('/api/v1/admin/analytics/traffic', { period });

// ── User moderation ────────────────────────────────────────

export const shadowbanUser = (userId: string) =>
	post<{ id: string; isShadowbanned: boolean }>(`/api/v1/admin/users/${userId}/shadowban`);

export const banUser = (userId: string) => post<{ id: string; isBanned: boolean }>(`/api/v1/admin/users/${userId}/ban`);

export const unbanUser = (userId: string) =>
	post<{ id: string; isBanned: boolean }>(`/api/v1/admin/users/${userId}/unban`);

// Schedules the erasure (task 77.3): the account is blocked now and erased
// when the window ends; the user row offers the undo (cancelErasure).
export const deleteUser = (userId: string) => del<{ id: string; eraseAt: string }>(`/api/v1/admin/users/${userId}`);

// Screen time analytics
export const getScreenTimeAnalytics = (period: '7d' | '30d' = '7d') =>
	get<any>(`/api/v1/admin/analytics/screen-time?period=${period}`);
export const getScreenTimeUser = (userId: string) => get<any>(`/api/v1/admin/analytics/screen-time/user/${userId}`);
