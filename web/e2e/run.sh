#!/usr/bin/env bash
set -euo pipefail
web="$(cd "$(dirname "$0")/.." && pwd)"
root="$(cd "$web/.." && pwd)"
project="${RELIC_UI_E2E_PROJECT:-relic-ui}"
version="$(node -p "require('$web/node_modules/@playwright/test/package.json').version")"
compose=(docker compose -p "$project" -f "$root/deploy/docker-compose.yml" -f "$root/test/compose/plain-http.yml")
export RELIC_PORT="${RELIC_UI_E2E_PORT:-28097}" RELIC_POSTGRESQL_PORT="${RELIC_UI_E2E_POSTGRESQL_PORT:-25597}" RELIC_OBJECT_STORAGE_PORT="${RELIC_UI_E2E_OBJECT_STORAGE_PORT:-28397}"

# RELIC_UI_E2E_STACK names the Compose project of a running stack to test
# instead of starting one.
if [[ -n "${RELIC_UI_E2E_STACK:-}" ]]; then
  project="$RELIC_UI_E2E_STACK"
else
  cleanup() { [[ "${KEEP:-}" == 1 ]] || "${compose[@]}" down -v >/dev/null 2>&1 || true; }
  trap cleanup EXIT
  # Two images built at once from one Dockerfile can collide on a locked
  # layer: BuildKit then fails one build instead of waiting.
  "${compose[@]}" build relic-server relic-worker >/dev/null || "${compose[@]}" build relic-server relic-worker >/dev/null
  "${compose[@]}" up -d --wait relic-server relic-worker >/dev/null
  for _ in $(seq 60); do curl -sf "http://127.0.0.1:$RELIC_PORT/api/v1/version" >/dev/null && break; sleep 1; done
  curl -sf "http://127.0.0.1:$RELIC_PORT/api/v1/version" >/dev/null || { echo "relic-server did not answer on port $RELIC_PORT within 60s" >&2; exit 1; }
fi

rm -rf "$web/e2e/results"
docker run --rm --network "${project}_default" --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e RELIC_UI_URL=http://relic-server:8090 -v "$web:/web" -w /web \
  "mcr.microsoft.com/playwright:v$version-noble" \
  node node_modules/@playwright/test/cli.js test -c e2e/playwright.config.ts "$@"
