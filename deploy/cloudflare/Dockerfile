# The image Wrangler pushes to Cloudflare's registry; wrangler.jsonc sets RELIC_VERSION.
ARG RELIC_VERSION
FROM ghcr.io/relicbuild/relic:${RELIC_VERSION}
CMD ["server", "start", "--worker", "--no-tls"]
