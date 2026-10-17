#!/usr/bin/env bash
# covers: rt upload, rt download
# tier: platform (the OpenTelemetry Collector of the Compose stack)
source "$(dirname "$0")/lib.sh"
: "${SCENARIO_METRICS_URL:?the metrics port, e.g. http://relic-server:8091}"
: "${SCENARIO_TRACES_FILE:?the file the collector writes}"
repo=relic-scenario-telemetry

# value sums the samples of the series matching an extended regex (the
# name and label part of a line of the Prometheus text format).
value() {
  curl -sf "$SCENARIO_METRICS_URL/metrics" | { grep -E "^$1" || true; } | awk '{s += $NF} END {printf "%d\n", s}'
}

step "/metrics on the metrics port only"
curl -sf "$SCENARIO_METRICS_URL/metrics" >metrics.txt
grep -q '^relic_build_info{component="server"' metrics.txt
grep -q '^go_goroutines' metrics.txt
[[ "$(curl -s "$SCENARIO_URL/metrics")" != *relic_build_info* ]]

uploads='relic_artifact_uploads_total\{[^}]*format="generic"'
downloads='relic_artifact_downloads_total\{[^}]*source="local"'
puts='relic_http_requests_total\{[^}]*code="2xx",family="packages",method="PUT"'
signins='relic_auth_attempts_total\{method="basic",result="success"'
before_up=$(value "$uploads") before_down=$(value "$downloads") before_put=$(value "$puts") before_auth=$(value "$signins")

step "jf rt upload and download move the counters"
delete_repo "$repo"
create_generic_repo "$repo"
echo "telemetry $(date +%s%N)" >file.txt
jf rt u file.txt "$repo/t/"
jf rt dl "$repo/t/file.txt" got/ --flat
cmp file.txt got/file.txt
for c in "uploads $uploads $before_up" "downloads $downloads $before_down" "PUTs $puts $before_put" "sign-ins $signins $before_auth"; do
  read -r name re before <<<"$c"
  after=$(value "$re")
  if ((after <= before)); then
    echo "$name did not move: $before -> $after" >&2
    exit 1
  fi
done

step "the collector receives the request's trace"
trace=$(openssl rand -hex 16)
curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -H "traceparent: 00-$trace-$(openssl rand -hex 8)-01" \
  -T file.txt "$SCENARIO_URL/artifactory/$repo/t/traced.txt" >/dev/null
for _ in $(seq 60); do
  grep -q "\"traceId\":\"$trace\"" "$SCENARIO_TRACES_FILE" 2>/dev/null && break
  sleep 1
done
grep -q "\"traceId\":\"$trace\"" "$SCENARIO_TRACES_FILE"
# The server span is named after the route, and the deploy has its own span.
grep "\"traceId\":\"$trace\"" "$SCENARIO_TRACES_FILE" >trace.jsonl
grep -q '"name":"PUT /artifactory/"\|"name":"/artifactory/"' trace.jsonl
grep -q '"name":"repo.Deploy"' trace.jsonl

delete_repo "$repo"
