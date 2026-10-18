#!/usr/bin/env bash
# Runs one test tier against its own stack:
#   test/tier.sh core       the single binary: core scenarios, the SDK suite, the UI e2e, a storage check
#   test/tier.sh platform   the Compose stack: platform scenarios, a storage check
#   test/tier.sh k8s        one kind cluster: the Helm chart and everything Kubernetes-specific (test/k8s/run.sh)
# Scenario names after the tier narrow its scenarios to those named.
# Each tier has its own Compose project, port and results directory, so
# tiers can run at the same time.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
tier="${1:?core, platform or k8s}"
e2e="$root/api/adapters/artifactory/compat/e2e"

base="${RELIC_E2E_PORT_BASE:-28090}"
case "$tier" in
core) port=$((base + 1)) single=true ;;
platform) port=$((base + 2)) single=false ;;
k8s) port=$((base + 3)) single=true ;;
*) echo "unknown tier $tier: core, platform or k8s" >&2; exit 2 ;;
esac
project="relic-$tier"
export RELIC_POSTGRESQL_PORT="$((port - 2500))" RELIC_OBJECT_STORAGE_PORT="$((port + 300))"

# KEEP=1 leaves the stack running. The vulnerability database outlives the
# stack: downloading it again takes about a minute.
down() {
  [[ "${KEEP:-}" == 1 ]] && return
  docker compose -p "$project" -f "$root/deploy/docker-compose.yml" down >/dev/null 2>&1 || true
  docker volume ls -q --filter "label=com.docker.compose.project=$project" | grep -v '_vulnerability-database$' |
    xargs -r docker volume rm >/dev/null 2>&1 || true
}
# The server's and worker's output and a last metrics scrape go into the
# tier's results, which CI uploads, before the stack is removed.
keep_server_output() {
  [[ "$tier" == k8s ]] && return
  local logs="$e2e/results/$tier/logs"
  mkdir -p "$logs"
  for service in relic-server relic-worker; do
    docker compose -p "$project" -f "$root/deploy/docker-compose.yml" logs --no-color --timestamps "$service" \
      >"$logs/$service.log" 2>/dev/null || true
  done
  docker run --rm --network "${project}_default" curlimages/curl:8.23.0 -sf http://relic-server:8091/metrics \
    >"$logs/relic-server.metrics" 2>/dev/null || true
}
temporary=()
cleanup() {
  rm -rf "${temporary[@]}"
  keep_server_output
  down
}
trap cleanup EXIT

storage_check() {
  local dir; dir="$(mktemp -d)"
  temporary+=("$dir")
  local config="$dir/config.yaml"
  local server="http://127.0.0.1:$port"
  printf 'password\n' | (cd "$root" && go run ./cmd/relic --config "$config" auth login "$server" \
    --user admin --password-stdin >/dev/null) || return 1
  (cd "$root" && go run ./cmd/relic --config "$config" --url "$server" storage check --wait -o json)
}

status=0
KEEP= down
if [[ "$tier" != k8s ]]; then
  for host_port in "$port" "$RELIC_POSTGRESQL_PORT" "$RELIC_OBJECT_STORAGE_PORT"; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$host_port") 2>/dev/null; then
      echo "tier $tier: host port $host_port is in use; stop what holds it or set RELIC_E2E_PORT_BASE (now $base)" >&2
      exit 2
    fi
  done
fi
if [[ "$tier" == k8s ]]; then
  compose=(docker compose -p "$project" -f "$root/deploy/docker-compose.yml")
  "${compose[@]}" build relic-server
  docker build -q -t "$project-e2e" --build-arg RELIC_IMAGE="$project-relic-server:latest" \
    -f "$e2e/Dockerfile" "$root" >/dev/null
  # One platform per archive: kind imports every platform of an index, and
  # a locally pulled image holds only its own.
  images="$(mktemp -d)"
  temporary+=("$images")
  docker tag "$project-relic-server:latest" relic:k8s
  for img in relic:k8s chrislusf/seaweedfs:4.48 curlimages/curl:8.23.0 alpine/k8s:1.37.1; do
    docker image inspect "$img" >/dev/null 2>&1 || docker pull -q "$img" >/dev/null
    docker save --platform linux/amd64 -o "$images/$(tr '/:' '__' <<<"$img").tar" "$img"
  done
  docker run --rm --network host -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$root/helm/relic:/chart:ro" -v "$root/test/k8s:/k8s:ro" -v "$images:/images:ro" \
    -e KEEP -e KIND_NODE_IMAGE --entrypoint bash "$project-e2e" /k8s/run.sh || status=1
  rm -rf "$images"
  exit "$status"
fi
RELIC_E2E_TIER="$tier" RELIC_E2E_PROJECT="$project" RELIC_PORT="$port" RELIC_E2E_SINGLE_NODE="$single" \
  RELIC_E2E_RESULTS="$e2e/results/$tier" "$e2e/run.sh" "${@:2}" &
scenarios=$!
if [[ "$tier" == core ]]; then
  # The UI e2e shares the stack with the scenarios once the server answers.
  until curl -sf "http://127.0.0.1:$port/api/v1/version" >/dev/null; do
    kill -0 "$scenarios" 2>/dev/null || break
    sleep 2
  done
  RELIC_UI_E2E_STACK="$project" "$root/web/e2e/run.sh" || status=1
fi
wait "$scenarios" || status=1
storage_check || status=1
exit "$status"
