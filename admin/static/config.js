// Runtime configuration for the admin panel.
//
// This file is a PLACEHOLDER. It ships with the build and is overwritten at
// container start by docker-entrypoint.sh, which renders it from
// $NEKKO_API_URL. That indirection is the whole point: the panel's image is
// built once and told at run time which API it serves — a local docker stack
// now, its own host later — instead of baking an origin into the bundle.
//
// It deliberately assigns NOTHING here: $lib/config/api falls through to the
// build-time VITE_API_URL when the object is absent, which is what
// `npm run dev` wants. An empty apiUrl would win over that fallback and point
// the panel at itself.
