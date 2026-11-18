import { onDestroy } from 'svelte';
import { freshAnswers } from '$lib/api/client';

/**
 * Runs `reload` whenever an answer the page was handed from memory comes back
 * different from the hub (lib/api/client.ts, freshAnswers). The page draws the
 * kept answer at once and redraws with the new one a moment later. Call during
 * component init.
 */
export function onFreshAnswers(reload: () => unknown): void {
	let first = true;
	const stop = freshAnswers.subscribe(() => {
		if (first) {
			first = false;
			return;
		}
		void reload();
	});
	onDestroy(stop);
}
