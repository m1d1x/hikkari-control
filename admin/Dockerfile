# The admin panel (nekko-admin) as its own image.
#
# It is a static SvelteKit SPA (adapter-static) and nothing more: it holds no
# server of its own and reaches the API the same way a browser on a laptop
# does — cross-origin, over the network. That is deliberate rather than
# incidental. The panel is meant to move to its own host (adminus.<base>)
# without any code change, so it is never allowed to depend on sitting next
# to the API.
#
# Build context is `admin/`, not the repo root: this image shares nothing with
# the Go build, and the root .dockerignore whitelists only what the Go builder
# needs.

FROM --platform=$BUILDPLATFORM node:22-alpine AS build
WORKDIR /app

# Dependencies first, so a source-only change does not re-resolve the tree.
COPY package.json package-lock.json ./
RUN npm ci

COPY . .

# VITE_API_URL is intentionally NOT passed here. The API origin is a runtime
# question answered by docker-entrypoint.sh below — see src/lib/config/api.ts.
RUN npm run build

FROM nginx:1.27-alpine

COPY nginx.conf /etc/nginx/conf.d/default.conf
# The base image ships its own index.html / 50x.html; left in place they would
# answer for paths the panel owns (`/` above all).
RUN rm -rf /usr/share/nginx/html/*
COPY --from=build /app/build /usr/share/nginx/html

# The nginx image runs every executable in /docker-entrypoint.d/ before
# starting the server, which is exactly the hook we need: render config.js
# from the environment, then serve.
COPY docker-entrypoint.sh /docker-entrypoint.d/40-nekko-admin-config.sh
RUN chmod +x /docker-entrypoint.d/40-nekko-admin-config.sh

EXPOSE 80

HEALTHCHECK --interval=5s --timeout=3s --retries=12 --start-period=5s \
	CMD wget -qO- http://127.0.0.1/config.js >/dev/null || exit 1
