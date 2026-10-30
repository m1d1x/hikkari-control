/*
 * Chart colours for the anti-blocking console.
 *
 * The CSS tokens in `$lib/styles/entry.css` are oklch, which ECharts cannot
 * read and which cannot carry the `${color}38` alpha suffix the gradients
 * use. These are the same hues as hex, and they are the only colours the
 * section's charts draw with — a chart that picks its own colour breaks the
 * tie between a legend line and the breakdown line beside it.
 */
export const CHART = {
	ok: '#4ade80',
	warn: '#fbbf24',
	bad: '#f43f5e',
	info: '#38bdf8',
	violet: '#a78bfa',
	pink: '#f472b6',
	dim: '#71717a',
} as const;

/** The zone colours, assigned in a fixed order so a TLD keeps its colour. */
export const ZONE_COLORS: readonly string[] = [CHART.info, CHART.warn, CHART.violet, CHART.pink, CHART.ok, CHART.dim];

/** The colour a top-level domain draws with, stable across renders. */
export function zoneColor(tld: string, all: readonly string[]): string {
	const i = all.indexOf(tld);
	return ZONE_COLORS[(i < 0 ? 0 : i) % ZONE_COLORS.length];
}
