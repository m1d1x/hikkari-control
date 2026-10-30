// Seeded pseudo-random generator for deterministic mock data
export function seededRandom(seed: number) {
	let s = seed;
	return () => {
		s = (s * 1664525 + 1013904223) & 0xffffffff;
		return (s >>> 0) / 0xffffffff;
	};
}

const rng = seededRandom(42);

export function randomInt(min: number, max: number): number {
	return Math.floor(rng() * (max - min + 1)) + min;
}

export function randomFloat(min: number, max: number, decimals = 2): number {
	return parseFloat((rng() * (max - min) + min).toFixed(decimals));
}

export function randomItem<T>(arr: T[]): T {
	return arr[Math.floor(rng() * arr.length)];
}

export function generateTimeSeries(
	days: number,
	baseValue: number,
	variance: number,
	trend: number = 0,
): { timestamp: string; value: number }[] {
	const points: { timestamp: string; value: number }[] = [];
	const now = new Date();

	for (let i = days - 1; i >= 0; i--) {
		const date = new Date(now);
		date.setDate(date.getDate() - i);
		const trendFactor = 1 + (trend * (days - i)) / days;
		const value = Math.max(0, Math.round((baseValue + (rng() - 0.5) * 2 * variance) * trendFactor));
		points.push({
			timestamp: date.toISOString().split('T')[0],
			value,
		});
	}

	return points;
}

export function generateSparkData(length: number, base: number, variance: number): number[] {
	return Array.from({ length }, () => Math.max(0, Math.round(base + (rng() - 0.5) * 2 * variance)));
}

const adjectives = ['Nova', 'Cyber', 'Pixel', 'Neon', 'Digital', 'Quantum', 'Shadow', 'Crystal', 'Storm', 'Phantom'];
const nouns = ['Haven', 'Nexus', 'Realm', 'Zone', 'Hub', 'Arena', 'Forge', 'Den', 'Base', 'Lab'];

export function generateServerName(): string {
	return randomItem(adjectives) + ' ' + randomItem(nouns);
}

const userPrefixes = ['user', 'dev', 'mod', 'admin', 'cool', 'pro', 'neo', 'dark', 'star', 'fire'];
const userSuffixes = ['_x', '42', '_dev', 'gg', '_nk', '99', '_z', '007', '_pro', '1337'];

export function generateUsername(): string {
	return randomItem(userPrefixes) + randomItem(userSuffixes);
}

export function generateUUID(): string {
	return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
		const r = (rng() * 16) | 0;
		const v = c === 'x' ? r : (r & 0x3) | 0x8;
		return v.toString(16);
	});
}

export function daysAgo(days: number): string {
	const d = new Date();
	d.setDate(d.getDate() - days);
	return d.toISOString();
}

export function hoursAgo(hours: number): string {
	const d = new Date();
	d.setHours(d.getHours() - hours);
	return d.toISOString();
}

export function minutesAgo(minutes: number): string {
	const d = new Date();
	d.setMinutes(d.getMinutes() - minutes);
	return d.toISOString();
}
