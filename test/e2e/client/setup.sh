#!/usr/bin/env bash
# Prepares the client container once, before any scenario: trusts the TLS
# proxy's certificate authority and writes the jf configuration every
# scenario starts from.
set -euo pipefail
: "${RELIC_URL:?}"

auth=()
if [[ -n "${RELIC_TOKEN:-}" ]]; then
  auth=(--access-token "$RELIC_TOKEN")
else
  auth=(--user "${RELIC_USER:-admin}" --password "${RELIC_PASSWORD:-password}" --basic-auth-only --enc-password=false)
fi
if [[ -n "${TLS_CA:-}" && -f "$TLS_CA" ]]; then
  cp "$TLS_CA" /usr/local/share/ca-certificates/relic-e2e.crt && update-ca-certificates >/dev/null
fi
jf config add relic --url "$RELIC_URL" "${auth[@]}" --interactive=false --overwrite >/dev/null
if [[ -n "${SCENARIO_TLS_URL:-}" ]]; then
  jf config add relic-tls --url "$SCENARIO_TLS_URL" "${auth[@]}" --interactive=false --overwrite >/dev/null
  jf config use relic >/dev/null
fi
