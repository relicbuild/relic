#!/usr/bin/env bash
# covers: api docs, api docs search, api docs describe
source "$(dirname "$0")/lib.sh"

step "jf api docs"
jf api docs >/dev/null

step "jf api docs search"
jf api docs search ping --method GET --format json >search.json
jq -e '.matches | length > 0' search.json >/dev/null
suggested=$(jq -r '.matches[] | select(.path == "/artifactory/api/system/ping") | .jf_api' search.json)
[[ "$suggested" == "jf api /artifactory/api/system/ping" ]]

step "jf api docs describe"
jf api docs describe GET /artifactory/api/system/ping --format json |
  jq -e '.method == "GET" and .path == "/artifactory/api/system/ping"' >/dev/null

step "the suggested command against Relic"
read -ra command <<<"$suggested"
[[ "$("${command[@]}")" == "OK" ]]

step "Access v2 groups through jf api, against Relic"
group="relic-scenario-api-group${SCENARIO_SUFFIX:-}"
jf api /access/api/v2/groups/"$group" -X DELETE >/dev/null 2>&1 || true
jf api /access/api/v2/groups -X POST -H "Content-Type: application/json" -d "{\"name\":\"$group\",\"description\":\"from jf api\"}" |
  jq -e --arg g "$group" '.name == $g and .description == "from jf api"' >/dev/null
jf api /access/api/v2/groups | jq -e --arg g "$group" '[.groups[].group_name] | index($g) != null' >/dev/null
jf api /access/api/v2/groups/"$group" -X DELETE >/dev/null
