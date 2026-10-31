// Formatting and the time windows shared by the product screens, real and mock.

export type Win = '24h' | '7d' | '30d';
export const WINDOWS: Win[] = ['24h', '7d', '30d'];

/** Grouped with a thin space: the locale's full-width one gapes in a mono font. */
export const num = (v: number) => Math.round(v).toLocaleString('ru').replace(/\s/g, ' ');
export const rub = (v: number) => `${num(v)} ₽`;
export const pct = (v: number) => `${v.toFixed(1)}%`;

export function compact(v: number): string {
	if (v >= 1e6) return `${(v / 1e6).toFixed(1)}m`;
	if (v >= 1e4) return `${Math.round(v / 1e3)}k`;
	if (v >= 1e3) return `${(v / 1e3).toFixed(1)}k`;
	return String(Math.round(v));
}

/** A duration in seconds, as short as it reads: 0.8 с, 42 с, 7 мин, 3 ч, 2 дн. */
export function secs(v: number): string {
	if (v < 10) return `${v.toFixed(1)} с`;
	if (v < 90) return `${Math.round(v)} с`;
	if (v < 90 * 60) return `${Math.round(v / 60)} мин`;
	if (v < 48 * 3600) return `${Math.round(v / 3600)} ч`;
	return `${Math.round(v / 86400)} дн`;
}

/** The axis label of a bucket and its tooltip title. */
export function bucketText(t: number, w: Win): { axis: string; tip: string } {
	const d = new Date(t);
	const hh = `${String(d.getHours()).padStart(2, '0')}:00`;
	const dm = `${String(d.getDate()).padStart(2, '0')}.${String(d.getMonth() + 1).padStart(2, '0')}`;
	if (w === '24h') return { axis: hh, tip: `${dm} ${hh}` };
	if (w === '7d') return { axis: dm, tip: `${dm} ${hh}` };
	return { axis: dm, tip: dm };
}

export function ago(t: number): string {
	const m = Math.round((Date.now() - t) / 60_000);
	if (m < 1) return 'сейчас';
	if (m < 60) return `${m} мин`;
	if (m < 48 * 60) return `${Math.round(m / 60)} ч`;
	return `${Math.round(m / 1440)} дн`;
}
