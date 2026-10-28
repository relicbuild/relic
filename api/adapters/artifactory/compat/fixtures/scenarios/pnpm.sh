#!/usr/bin/env bash
# covers: pnpm, pnpm-config
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v pnpm >/dev/null; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER or pnpm missing" >&2
  exit 0
fi
LOCAL=relic-scenario-pnpm-local
REMOTE=relic-scenario-pnpm-remote
VIRTUAL=relic-scenario-pnpm
for r in "$VIRTUAL" "$REMOTE" "$LOCAL"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"npm"},
 {"key":"$REMOTE","rclass":"remote","packageType":"npm","url":"https://registry.npmjs.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"npm","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
B=relic-scenario-pnpm-build
jf rt bdi "$B" --max-builds 0 >/dev/null 2>&1 || true

registry="$SCENARIO_URL/artifactory/api/npm/$VIRTUAL/"
auth=$(printf '%s:%s' "$SCENARIO_USER" "$SCENARIO_PASSWORD" | base64 -w0)
printf 'registry=%s\n%s:_auth=%s\n' "$registry" "${registry#*:}" "$auth" >.npmrc
export npm_config_store_dir="$WORK/pnpm-store"

step "pnpm install with build info"
printf '{"name":"relic-scenario-pnpm-app","version":"1.0.0","dependencies":{"is-number":"7.0.0"}}\n' >package.json
jf pnpmc --server-id-resolve relic --repo-resolve "$VIRTUAL"
jf pnpm install --build-name "$B" --build-number 1
test -d node_modules/is-number
step "pnpm publish with build info"
printf 'module.exports = 1;\n' >index.js
jf pnpm publish --no-git-checks --build-name "$B" --build-number 1
jf rt s "$LOCAL/relic-scenario-pnpm-app/-/*" | expect_count 1
jf rt bp "$B" 1
jf rt curl -s "/api/build/$B/1" | jq -e '.buildInfo.modules | length >= 1'

jf rt bdi "$B" --max-builds 0 >/dev/null 2>&1 || true
for r in "$VIRTUAL" "$REMOTE" "$LOCAL"; do delete_repo "$r"; done
