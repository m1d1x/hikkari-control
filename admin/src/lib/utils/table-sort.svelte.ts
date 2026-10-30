/**
 * Client-side sort state for a table whose rows are already in memory.
 *
 * The admin's tables are small aggregates (geo, browsers, sessions) that
 * arrive in one response, so sorting them means reordering an array, not
 * another round-trip. One instance per table: the state is the pair
 * (column, direction), and `apply` returns a sorted copy.
 */
export class TableSort {
	by = $state('');
	dir: 'asc' | 'desc' = $state('desc');

	constructor(by = '', dir: 'asc' | 'desc' = 'desc') {
		this.by = by;
		this.dir = dir;
	}

	/** Click on a header: same column flips direction, a new one starts descending. */
	toggle = (key: string): void => {
		if (this.by === key) {
			this.dir = this.dir === 'asc' ? 'desc' : 'asc';
		} else {
			this.by = key;
			this.dir = 'desc';
		}
	};

	apply<T extends object>(rows: T[]): T[] {
		if (!this.by) return rows;
		const key = this.by;
		const sign = this.dir === 'asc' ? 1 : -1;
		return [...rows].sort(
			(a, b) => sign * compare((a as Record<string, unknown>)[key], (b as Record<string, unknown>)[key]),
		);
	}
}

function compare(a: unknown, b: unknown): number {
	if (a == null) return b == null ? 0 : 1;
	if (b == null) return -1;
	if (typeof a === 'number' && typeof b === 'number') return a - b;
	return String(a).localeCompare(String(b), 'ru', { numeric: true });
}
