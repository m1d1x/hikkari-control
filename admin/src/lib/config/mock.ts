/**
 * Fixture data instead of the live admin API — opt-in only.
 *
 * The default is the real backend: a build that forgets the flag must show real
 * users, not plausible fakes. The old default was the other way round and the
 * container build (admin/Dockerfile) never set the flag, so every image —
 * the stand's included — served fixtures that looked like data.
 *
 * Enable for UI work without a backend: `VITE_USE_MOCK=true npm run dev`.
 */
export const USE_MOCK: boolean = import.meta.env.VITE_USE_MOCK === 'true';
