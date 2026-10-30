export function formatNumber(value: number, decimals = 0): string {
	if (Math.abs(value) >= 1_000_000) {
		return (value / 1_000_000).toFixed(1).replace(/\.0$/, '') + 'M';
	}
	if (Math.abs(value) >= 1_000) {
		return (value / 1_000).toFixed(1).replace(/\.0$/, '') + 'K';
	}
	return value.toFixed(decimals).replace(/\.0+$/, '');
}

export function formatPercent(value: number, decimals = 1): string {
	return value.toFixed(decimals) + '%';
}

export function formatDuration(ms: number): string {
	if (ms < 1000) return ms + 'ms';
	if (ms < 60_000) return (ms / 1000).toFixed(1) + 's';
	if (ms < 3_600_000) return Math.floor(ms / 60_000) + 'm ' + Math.floor((ms % 60_000) / 1000) + 's';
	return Math.floor(ms / 3_600_000) + 'h ' + Math.floor((ms % 3_600_000) / 60_000) + 'm';
}

export function formatBytes(bytes: number): string {
	if (bytes < 1024) return bytes + ' B';
	if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
	if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
	return (bytes / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
}

export function formatDate(date: string | Date, style: 'short' | 'long' | 'time' = 'short'): string {
	const d = typeof date === 'string' ? new Date(date) : date;
	if (style === 'time') {
		return d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
	}
	if (style === 'long') {
		return d.toLocaleDateString('ru-RU', { day: 'numeric', month: 'long', year: 'numeric' });
	}
	return d.toLocaleDateString('ru-RU', { day: '2-digit', month: '2-digit', year: '2-digit' });
}

export function formatRelativeTime(date: string | Date): string {
	const d = typeof date === 'string' ? new Date(date) : date;
	const diff = Date.now() - d.getTime();
	const seconds = Math.floor(diff / 1000);

	if (seconds < 60) return 'just now';
	if (seconds < 3600) return Math.floor(seconds / 60) + 'm ago';
	if (seconds < 86400) return Math.floor(seconds / 3600) + 'h ago';
	if (seconds < 604800) return Math.floor(seconds / 86400) + 'd ago';
	return formatDate(d, 'short');
}

export function formatUptimeSeconds(seconds: number): string {
	const days = Math.floor(seconds / 86400);
	const hours = Math.floor((seconds % 86400) / 3600);
	const minutes = Math.floor((seconds % 3600) / 60);

	if (days > 0) return `${days}d ${hours}h ${minutes}m`;
	if (hours > 0) return `${hours}h ${minutes}m`;
	return `${minutes}m`;
}

export function formatTrend(
	current: number,
	previous: number,
): { value: number; direction: 'up' | 'down' | 'neutral' } {
	if (previous === 0) return { value: 0, direction: 'neutral' };
	const change = ((current - previous) / previous) * 100;
	return {
		value: Math.abs(change),
		direction: change > 0.5 ? 'up' : change < -0.5 ? 'down' : 'neutral',
	};
}
