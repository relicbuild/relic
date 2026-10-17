#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../../../../.." && pwd)"
project="${RELIC_E2E_PROJECT:-relic}"
network="${project}_default"
image="${project}-relic-server:latest"
compose=(docker compose -p "$project" -f "$root/deploy/docker-compose.yml" -f "$root/test/compose/plain-http.yml")
services=(relic-server relic-worker)
if [[ "${RELIC_E2E_SINGLE_NODE:-false}" == true ]]; then
  compose+=(-f "$root/deploy/docker-compose.single-node.yml" -f "$root/test/compose/plain-http.single-node.yml")
  services=(relic-server)
fi
results="${RELIC_E2E_RESULTS:-$root/api/adapters/artifactory/compat/e2e/results}"
tls="$project-e2e-tls" otel="$project-e2e-otel"
scenario_dir="$root/api/adapters/artifactory/compat/fixtures/scenarios"
tier="${RELIC_E2E_TIER:-all}"
# RELIC_E2E_URL runs the scenarios against a server already running there,
# over HTTPS with a publicly trusted certificate, as RELIC_USER (an
# administrator) instead of starting the Compose stack.
remote="${RELIC_E2E_URL:-}"
remote="${remote%/}"
if [[ -n "$remote" && $remote != https://* ]]; then
  echo "RELIC_E2E_URL is $remote: an https:// address" >&2
  exit 2
fi
case "$tier" in
all | core | platform) ;;
*) echo "RELIC_E2E_TIER is $tier: all, core or platform" >&2; exit 2 ;;
esac
scenario_tier() {
  if [[ $1 == sdk-artifactory-rest ]]; then echo core; return; fi
  local t; t="$(sed -n 's/^# tier: *\([a-z]*\).*/\1/p' "$scenario_dir/$1.sh" | head -n1)"
  echo "${t:-core}"
}
in_tier() { [[ $tier == all || "$(scenario_tier "$1")" == "$tier" ]]; }
selected=()
for n in "$@"; do
  if in_tier "$n"; then selected+=("$n"); fi
done
if [[ $# -eq 0 ]]; then
  for f in "$scenario_dir"/*.sh; do
    n="$(basename "$f" .sh)"
    if [[ $n != lib ]] && in_tier "$n"; then selected+=("$n"); fi
  done
  if in_tier sdk-artifactory-rest; then selected+=(sdk-artifactory-rest); fi
fi
is_selected() { [[ " ${selected[*]} " == *" $1 "* ]]; }
own_daemon=()
for d in docker native-docker; do
  if is_selected "$d"; then own_daemon+=("$d"); fi
done
daemons=(shared "${own_daemon[@]}")
dind_name() { echo "$project-e2e-dind-$1"; }
dind_alias() { if [[ $1 == shared ]]; then echo relic-e2e-dind; else echo "relic-e2e-dind-$1"; fi; }

phase() { printf '[e2e %4ss] %s\n' "$SECONDS" "$*" >&2; }
phase "daemons"
docker network inspect "$network" >/dev/null 2>&1 ||
  docker network create --label com.docker.compose.network=default --label "com.docker.compose.project=$project" "$network" >/dev/null
for d in "${daemons[@]}"; do
  docker rm -f "$(dind_name "$d")" >/dev/null 2>&1 || true
  docker run -d --rm --privileged --name "$(dind_name "$d")" --network "$network" --network-alias "$(dind_alias "$d")" \
    -v "$(dind_name "$d"):/var/lib/docker" -e DOCKER_TLS_CERTDIR= docker:28-dind --insecure-registry relic-server:8090 >/dev/null
done
bin=
trap 'for d in "${daemons[@]}"; do docker rm -f "$(dind_name "$d")" >/dev/null 2>&1; done; docker rm -f "$tls" "$otel" >/dev/null 2>&1 || true; rm -rf "${bin:-}"' EXIT
client_images=(alpine:3.21 debian:bookworm-slim mono:6.12 nixos/nix:2.35.2 osixia/openldap:1.5.0 quay.io/podman/stable:v5 rockylinux/rockylinux:10.2
  ghcr.io/opencontainers/distribution-spec/conformance:v1.1.1)
prepare_daemon() {
  local c; c="$(dind_name "$1")"
  for _ in $(seq 60); do docker exec "$c" docker info >/dev/null 2>&1 && break; sleep 1; done
  docker exec "$c" docker info >/dev/null 2>&1 || { echo "docker daemon $1 ($c) not ready after 60 s" >&2; return 1; }
  local kept=()
  [[ $1 == shared ]] && kept=("${client_images[@]}")
  docker exec "$c" sh -c 'docker rm -f $(docker ps -aq) >/dev/null 2>&1; docker volume prune -af >/dev/null 2>&1
    printf "%s\n" "$@" >/tmp/kept
    docker images --format "{{.Repository}}:{{.Tag}}" | grep -v "<none>" | grep -vxFf /tmp/kept | xargs -r docker rmi >/dev/null 2>&1
    for i in "$@"; do docker images -q "$i"; done >/tmp/kept-ids
    docker images -qa | sort -u | grep -vxFf /tmp/kept-ids | xargs -r docker rmi -f >/dev/null 2>&1
    docker builder prune -af >/dev/null 2>&1
    for i in "$@"; do docker image inspect "$i" >/dev/null 2>&1 || docker pull -q "$i" >/dev/null 2>&1 & done
    wait; true' sh "${kept[@]}"
}
prepared=()
for d in "${daemons[@]}"; do prepare_daemon "$d" & prepared+=($!); done

phase "harness"
bin="$(mktemp -d)"
harness=(env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -tags e2e -o "$bin/e2e.test" ./api/adapters/artifactory/compat/e2e/harness)
if command -v go >/dev/null; then
  (cd "$root" && "${harness[@]}")
else
  docker run --rm -v "$root:/src:ro" -v "$bin:/out" -w /src -e GOFLAGS=-buildvcs=false -e GOCACHE=/tmp/go-cache \
    -e GOMODCACHE=/tmp/go-mod golang:1.27.1-alpine sh -c 'CGO_ENABLED=0 go test -c -tags e2e -o /out/e2e.test ./api/adapters/artifactory/compat/e2e/harness'
fi

export RELIC_OTLP_ENDPOINT="${RELIC_OTLP_ENDPOINT:-http://relic-e2e-otel:4318}"
export RELIC_FAILED_SIGN_INS_BEFORE_BACKOFF="${RELIC_FAILED_SIGN_INS_BEFORE_BACKOFF:-1000}"
if [[ -z "$remote" && "${RELIC_E2E_SINGLE_NODE:-false}" == true ]]; then
  "${compose[@]}" rm -sf relic-worker >/dev/null 2>&1 || true
fi
phase "server image"
# Two images built at once from one Dockerfile can collide on a locked layer:
# BuildKit then fails one build instead of waiting. A remote run needs the
# image only for the relic CLI the client image copies.
if [[ -n "$remote" ]]; then
  "${compose[@]}" build relic-server
else
  "${compose[@]}" build "${services[@]}" || "${compose[@]}" build "${services[@]}"
  phase "stack up"
  "${compose[@]}" up -d --wait --remove-orphans "${services[@]}"
fi
phase "client image"
docker build -q -t "$project-e2e" --build-arg RELIC_IMAGE="$image" -f "$root/api/adapters/artifactory/compat/e2e/Dockerfile" "$root" >/dev/null

# TLS in front of Relic (Caddy's internal CA): the go command refuses to
# send credentials to a plain-HTTP GOPROXY.
docker rm -f "$tls" >/dev/null 2>&1 || true
# The name has a dot: terraform refuses registry hostnames without one.
if [[ -z "$remote" ]]; then
  docker run -d --rm --name "$tls" --network "$network" --network-alias tls.relic.test caddy:2 \
    caddy reverse-proxy --from https://tls.relic.test --to relic-server:8090 --internal-certs >/dev/null
fi
for p in "${prepared[@]}"; do wait "$p"; done

phase "daemons ready"
rm -rf "$results" && mkdir -p "$results"
mkdir -p "$results/otel"
docker rm -f "$otel" >/dev/null 2>&1 || true
docker run -d --rm --name "$otel" --user "$(id -u):$(id -g)" --network "$network" --network-alias relic-e2e-otel \
  -v "$root/api/adapters/artifactory/compat/e2e/otel-collector.yaml:/etc/otelcol/config.yaml:ro" -v "$results/otel:/out" \
  otel/opentelemetry-collector-contrib:0.161.0 --config=/etc/otelcol/config.yaml >/dev/null
if [[ -n "$remote" ]]; then
  docker run --rm caddy:2 cat /etc/ssl/certs/ca-certificates.crt >"$results/tls-ca.crt"
fi
for _ in $(seq 30); do
  [[ -f "$results/tls-ca.crt" ]] && break
  docker cp "$tls":/data/caddy/pki/authorities/local/root.crt "$results/tls-ca.crt" >/dev/null 2>&1 && break
  sleep 1
done
[[ -f "$results/tls-ca.crt" ]] || { echo "Caddy's CA certificate not available after 30 s" >&2; exit 1; }
phase "scenarios"
server_env=(-e RELIC_URL=http://relic-server:8090 -e SCENARIO_REGISTRY=relic-server:8090 -e SCENARIO_TLS_URL=https://tls.relic.test
  -e SCENARIO_METRICS_URL=http://relic-server:8091 -e SCENARIO_TRACES_FILE=/results/otel/traces.json)
sdk_url="http://127.0.0.1:${RELIC_PORT:-8090}" sdk_container_url=http://relic-server:8090
if [[ -n "$remote" ]]; then
  server_env=(-e RELIC_URL="$remote" -e SCENARIO_REGISTRY="${remote#https://}" -e SCENARIO_REGISTRY_TLS=true
    -e SCENARIO_TLS_URL="$remote" -e SCENARIO_REMOTE_SERVER=true)
  sdk_url=$remote sdk_container_url=$remote
fi
sdk=0 scenarios=()
for s in "${selected[@]}"; do
  if [[ "$s" == sdk-artifactory-rest ]]; then sdk=1; else scenarios+=("$s"); fi
done
sdk_pid=
if [[ $sdk -eq 1 ]]; then
  mkdir -p "$results/logs"
  if command -v go >/dev/null; then
    sdk_test=(env -C "$root/api/adapters/artifactory/compat/sdk" RELIC_SDK_URL="$sdk_url" go test -tags=e2e -count=1 -v ./...)
  else
    sdk_test=(docker run --rm --network "$network" -v "$root:/src:ro" -w /src/api/adapters/artifactory/compat/sdk -e GOFLAGS=-buildvcs=false
      -e GOCACHE=/tmp/go-cache -e GOMODCACHE=/tmp/go-mod -e RELIC_SDK_URL="$sdk_container_url" -e RELIC_USER -e RELIC_PASSWORD
      golang:1.27.1-alpine go test -tags=e2e -count=1 -v ./...)
  fi
  ( if "${sdk_test[@]}" >"$results/logs/sdk-artifactory-rest.log" 2>&1; then echo pass; else echo fail; fi >"$results/sdk.result" ) &
  sdk_pid=$!
fi
if [[ ${#scenarios[@]} -gt 0 ]]; then
  docker run --rm --network "$network" -v "$results:/results" \
    -v "$root/api/adapters/artifactory/compat/fixtures/scenarios:/scenarios:ro" -v "$bin:/e2e:ro" -e E2E_PARALLEL \
    "${server_env[@]}" -e RELIC_TOKEN -e RELIC_USER -e RELIC_PASSWORD \
    -e DOCKER_HOST=tcp://relic-e2e-dind:2375 -e SCENARIO_DOCKER_HOSTNAME=relic-e2e-dind -e E2E_OWN_DAEMON="${own_daemon[*]}" \
    -e TLS_CA=/results/tls-ca.crt \
    -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
    "$project-e2e" "${scenarios[@]}"
fi
[[ -f "$results/results.json" ]] || echo '{}' >"$results/results.json"

if [[ -n "$sdk_pid" ]]; then
  wait "$sdk_pid" || true
  r="$(cat "$results/sdk.result" 2>/dev/null || echo fail)"
  echo "$r  sdk-artifactory-rest"
  jq --arg r "$r" '. + {"sdk-artifactory-rest": $r}' "$results/results.json" >"$results/results.json.tmp"
  mv "$results/results.json.tmp" "$results/results.json"
fi

phase "scenarios and SDK done"
failed=0
while read -r scenario; do
  [[ -z "$scenario" || "$scenario" == \#* ]] && continue
  is_selected "$scenario" || continue
  if [[ "$(jq -r --arg s "$scenario" '.[$s] // "missing"' "$results/results.json")" != pass ]]; then
    echo "required scenario failed: $scenario (see $results/logs/$scenario.log)" >&2
    failed=1
  fi
done <"$root/api/adapters/artifactory/compat/e2e/required.txt"
exit "$failed"
