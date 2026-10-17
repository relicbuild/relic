#!/usr/bin/env bash
# covers: yarn, yarn-config
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" ]] || ! command -v yarn >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL or yarn missing" >&2
  exit 0
fi
REMOTE=relic-scenario-yarn-remote
VIRTUAL=relic-scenario-yarn
for r in "$VIRTUAL" "$REMOTE"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$REMOTE","rclass":"remote","packageType":"npm","url":"https://registry.npmjs.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"npm","repositories":"$REMOTE"}]
JSON
jf rt rc repos.json
B=relic-scenario-yarn-build
jf rt bdi "$B" --max-builds 0 >/dev/null 2>&1 || true

step "yarn install with build info"
printf '{"name":"app","version":"1.0.0","private":true,"dependencies":{"is-number":"7.0.0"}}\n' >package.json
printf 'nodeLinker: node-modules\nenableTelemetry: false\n' >.yarnrc.yml
export YARN_ENABLE_IMMUTABLE_INSTALLS=false YARN_GLOBAL_FOLDER="$WORK/yarn-global" # metadata cache, per scenario
yarn --version
jf yarnc --server-id-resolve relic-tls --repo-resolve "$VIRTUAL"
jf yarn install --build-name "$B" --build-number 1
test -f node_modules/is-number/package.json
jf rt bp "$B" 1 --server-id relic
jf rt curl -s "/api/build/$B/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 1'

jf rt bdi "$B" --max-builds 0 >/dev/null 2>&1 || true
for r in "$VIRTUAL" "$REMOTE"; do delete_repo "$r"; done
