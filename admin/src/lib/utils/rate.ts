/**
 * A throughput held in Mbit/s as a person reads it: the unit follows the size
 * (кбит/с below one megabit, Гбит/с from a thousand), three significant
 * digits, a decimal comma. 0.15 reads "150 кбит/с", not "0,15 Мбит/с". The
 * hub's rate() (internal/entry/rate.go) prints the same.
 */
export function formatRate(mbps: number, signed = false): string {
	if (!Number.isFinite(mbps)) return '— Мбит/с';
	let v = Math.abs(mbps);
	let unit = 'Мбит/с';
	if (v > 0 && v < 1) [v, unit] = [v * 1000, 'кбит/с'];
	else if (v >= 1000) [v, unit] = [v / 1000, 'Гбит/с'];
	const digits = v === 0 || v >= 100 ? 0 : v >= 10 ? 1 : 2;
	let s = v.toFixed(digits);
	if (digits > 0) s = s.replace(/0+$/, '').replace(/\.$/, '');
	// Rounding can carry into the next unit: 999,6 кбит/с reads 1 Мбит/с.
	if (s === '1000' && unit !== 'Гбит/с') [s, unit] = ['1', unit === 'кбит/с' ? 'Мбит/с' : 'Гбит/с'];
	s = s.replace('.', ',');
	if (s !== '0') {
		if (mbps < 0) s = '−' + s;
		else if (signed) s = '+' + s;
	}
	return `${s} ${unit}`;
}
