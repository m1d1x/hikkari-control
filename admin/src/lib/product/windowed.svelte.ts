import { describeApiError } from '$lib/api/client';
import type { Win } from './format';

/**
 * One screen's data for the selected window: refetched when the window
 * changes, with the last good answer kept on screen while the next loads.
 * An answer that arrives after the window moved on is dropped. Create it
 * during component init — the refetch is an $effect.
 */
export class Windowed<T> {
	win = $state<Win>('7d');
	data = $state<T | null>(null);
	error = $state<string | null>(null);
	loading = $state(false);

	#seq = 0;

	constructor(initial: Win, fetch: (w: Win) => Promise<T>) {
		this.win = initial;
		$effect(() => {
			const w = this.win;
			const seq = ++this.#seq;
			this.loading = true;
			fetch(w)
				.then((d) => {
					if (seq !== this.#seq) return;
					this.data = d;
					this.error = null;
				})
				.catch((e) => {
					if (seq !== this.#seq) return;
					this.error = describeApiError(e);
				})
				.finally(() => {
					if (seq === this.#seq) this.loading = false;
				});
		});
	}
}
