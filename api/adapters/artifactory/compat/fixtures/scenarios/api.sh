#!/usr/bin/env bash
# covers: api
source "$(dirname "$0")/lib.sh"
API_REPO=relic-scenario-api-local
delete_repo "$API_REPO"

step "jf api GET"
[[ "$(jf api /artifactory/api/system/ping)" == "OK" ]]
jf api /artifactory/api/system/version | jq -e '.version' >/dev/null

step "jf api PUT with --data and --input"
jf api -X PUT -H "Content-Type: application/json" -d '{"rclass":"local","packageType":"generic"}' "/artifactory/api/repositories/$API_REPO" >/dev/null
jf api "/artifactory/api/repositories/$API_REPO" | jq -e '.packageType == "generic"' >/dev/null
echo "hello from jf api" | jf api -X PUT --input - "/artifactory/$API_REPO/api/hello.txt" >/dev/null
[[ "$(jf api "/artifactory/$API_REPO/api/hello.txt")" == "hello from jf api" ]]
jf api "/artifactory/api/storage/$API_REPO/api/hello.txt" | jq -e '.size == "18"' >/dev/null

step "jf api fails on an error answer (400 for an unknown repository)"
if jf api "/artifactory/api/repositories/relic-scenario-missing" >/dev/null 2>missing.log; then
  echo "expected a non-zero exit for an error answer" >&2
  exit 1
fi
grep -q "returned 400 " missing.log || { cat missing.log >&2; exit 1; }

delete_repo "$API_REPO"
