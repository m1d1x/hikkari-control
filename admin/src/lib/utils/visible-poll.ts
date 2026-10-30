// visiblePoll re-runs `tick` on an interval while the tab is visible, and once
// immediately whenever it becomes visible again.
//
// Why a poll and not a live socket: the support conversations live on the
// messaging plane, which the panel does not speak — the old SFU-side support
// socket it used to open had no server left behind it, so its «live» updates
// never arrived. A few seconds of latency on a support thread costs nothing;
// a hidden tab asking every few seconds would, so a hidden tab does not ask.
//
// Returns the stop function; call it from the component's teardown.
export function visiblePoll(tick: () => void, intervalMs: number): () => void {
	let timer: ReturnType<typeof setInterval> | null = null;

	const start = () => {
		if (timer === null) timer = setInterval(tick, intervalMs);
	};
	const stop = () => {
		if (timer !== null) {
			clearInterval(timer);
			timer = null;
		}
	};
	const onVisibility = () => {
		if (document.visibilityState === 'visible') {
			tick();
			start();
		} else {
			stop();
		}
	};

	document.addEventListener('visibilitychange', onVisibility);
	if (document.visibilityState === 'visible') start();

	return () => {
		stop();
		document.removeEventListener('visibilitychange', onVisibility);
	};
}
