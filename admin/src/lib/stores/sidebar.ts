import { writable } from 'svelte/store';

function createSidebarStore() {
	const stored = typeof localStorage !== 'undefined' ? localStorage.getItem('sidebar-collapsed') : null;
	const { subscribe, set, update } = writable(stored === 'true');

	return {
		subscribe,
		toggle() {
			update((v) => {
				const next = !v;
				if (typeof localStorage !== 'undefined') {
					localStorage.setItem('sidebar-collapsed', String(next));
				}
				return next;
			});
		},
		set(value: boolean) {
			if (typeof localStorage !== 'undefined') {
				localStorage.setItem('sidebar-collapsed', String(value));
			}
			set(value);
		},
	};
}

export const sidebarCollapsed = createSidebarStore();
