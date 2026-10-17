#!/usr/bin/env bash
set -euo pipefail
: "${RELIC_URL:?}"
trap '[[ -n "${HOST_UID:-}" ]] && chown -R "$HOST_UID:${HOST_GID:-$HOST_UID}" /results' EXIT

auth=()
if [[ -n "${RELIC_TOKEN:-}" ]]; then
  auth=(--access-token "$RELIC_TOKEN")
else
  auth=(--user "${RELIC_USER:-admin}" --password "${RELIC_PASSWORD:-password}" --basic-auth-only --enc-password=false)
fi
if [[ -n "${TLS_CA:-}" && -f "$TLS_CA" ]]; then
  cp "$TLS_CA" /usr/local/share/ca-certificates/relic-e2e.crt && update-ca-certificates >/dev/null
fi
jf config add relic --url "$RELIC_URL" "${auth[@]}" --interactive=false --overwrite
if [[ -n "${SCENARIO_TLS_URL:-}" ]]; then
  jf config add relic-tls --url "$SCENARIO_TLS_URL" "${auth[@]}" --interactive=false --overwrite
  jf config use relic >/dev/null
fi
export SCENARIO_URL="$RELIC_URL" SCENARIO_USER="${RELIC_USER:-admin}" SCENARIO_PASSWORD="${RELIC_PASSWORD:-password}"

SCENARIOS="$*" /e2e/e2e.test -test.run "^TestScenarios$" -test.v -test.count=1 -test.timeout=90m \
  -test.parallel="${E2E_PARALLEL:-$(nproc)}" -jf-home="$HOME/.jfrog" || true
