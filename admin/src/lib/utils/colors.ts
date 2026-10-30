// Chart color palette — hex for ECharts canvas compatibility
// Visually matches oklch tokens in tokens.css
export const chartColors = [
	'#e8699a', // pink (primary)
	'#45b97c', // green
	'#6192d4', // blue
	'#c9a94e', // yellow
	'#d07040', // orange
	'#ab5abf', // purple
	'#44b5b0', // cyan
	'#8f7070', // muted
];

export const statusColors = {
	healthy: '#45b97c',
	warning: '#c9a94e',
	critical: '#d4553e',
	unknown: '#7e7e7e',
} as const;

export const trendColors = {
	up: '#45b97c',
	down: '#d4553e',
	neutral: '#7e7e7e',
} as const;
