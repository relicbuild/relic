#!/usr/bin/env bash
# covers: audit, build-scan
# tier: platform (Grype runs in the separate worker)
source "$(dirname "$0")/lib.sh"
if ! command -v npm >/dev/null; then
  echo "skipped: npm missing" >&2
  exit 0
fi

step "jf audit finds lodash 4.17.15's advisories"
mkdir app && cd app
printf '{"name":"relic-audit","version":"1.0.0","dependencies":{"lodash":"4.17.15"}}\n' >package.json
npm install --package-lock-only --ignore-scripts --no-audit --no-fund >/dev/null
jf audit --npm --format json >audit.json
jq -e '[.[] | .vulnerabilities[]? | select(.components | has("npm://lodash:4.17.15"))] | length >= 1' audit.json >/dev/null
jq -e '[.[] | .vulnerabilities[]? | .issue_id | startswith("RELIC-")] | all' audit.json >/dev/null

step "table output and exit code"
jf audit --npm >table.txt
grep -q "lodash" table.txt

step "jf bs --vuln scans a published npm build"
NPM=relic-scenario-audit-npm
xr() { curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -H 'Content-Type: application/json' "$SCENARIO_URL/xray/api/v2/$1" "${@:2}"; }
xr watches/relic-scenario-watch -XDELETE >/dev/null 2>&1 || true
xr policies/relic-scenario-policy -XDELETE >/dev/null 2>&1 || true

delete_repo "$NPM"
printf '{"key":"%s","rclass":"remote","packageType":"npm","url":"https://registry.npmjs.org"}\n' "$NPM" >npm.json
jf rt repo-create npm.json
jf npmc --server-id-resolve relic --repo-resolve "$NPM"
jf npm install --ignore-scripts --no-audit --no-fund --build-name "$BUILD-audit" --build-number 1
jf rt bp "$BUILD-audit" 1
jf bs "$BUILD-audit" 1 --vuln --format json >bs.json
jq -e '[.. | objects | select(has("components")) | .components | keys[]] | index("npm://lodash:4.17.15") != null' bs.json >/dev/null
if jf bs "$BUILD-audit" 1 >default.txt 2>&1; then echo "jf bs without policies should fail" >&2; exit 1; fi
grep -q "Fail build in case of a violation" default.txt

step "policies and watches: violations fail jf audit and jf bs with exit code 3"
xr watches/relic-scenario-watch -XDELETE >/dev/null 2>&1 || true
xr policies/relic-scenario-policy -XDELETE >/dev/null 2>&1 || true
xr policies -d '{"name":"relic-scenario-policy","type":"security","rules":[{"name":"medium-up","priority":1,"criteria":{"min_severity":"Medium"},"actions":{"fail_build":true}}]}' >/dev/null
xr watches -d '{"general_data":{"name":"relic-scenario-watch","active":true},"project_resources":{"resources":[{"type":"build","name":"'"$BUILD-audit"'"}]},"assigned_policies":[{"name":"relic-scenario-policy","type":"security"}]}' >/dev/null
set +e
jf audit --npm --watches relic-scenario-watch >watch.txt 2>&1
audit_rc=$?
jf bs "$BUILD-audit" 1 >bs-violations.txt 2>&1
bs_rc=$?
set -e
grep -q "relic-scenario-watch" watch.txt
test "$audit_rc" = 3 || { echo "jf audit exit code $audit_rc, want 3" >&2; exit 1; }
test "$bs_rc" = 3 || { echo "jf bs exit code $bs_rc, want 3" >&2; cat bs-violations.txt >&2; exit 1; }
xr watches/relic-scenario-watch -XDELETE >/dev/null
xr policies/relic-scenario-policy -XDELETE >/dev/null

delete_repo "$NPM"
