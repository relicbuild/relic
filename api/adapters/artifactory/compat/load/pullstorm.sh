#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/../../../../.." && pwd)
export RELIC_PORT=28093 RELIC_POSTGRESQL_PORT=15435 RELIC_OBJECT_STORAGE_PORT=18336
upstream_port=28094
project=relic-pullstorm
compose=(docker compose -p "$project" -f "$root/deploy/docker-compose.yml" -f "$root/test/compose/plain-http.yml")
cleanup() { "${compose[@]}" down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT
step() { printf '\n==> %s\n' "$*" >&2; }

step "start the stack"
"${compose[@]}" up -d --build --wait relic-server >/dev/null
for _ in $(seq 60); do curl -fsS "localhost:$RELIC_PORT/readyz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "localhost:$RELIC_PORT/readyz" >/dev/null 2>&1 || { echo "relic-server not ready after 60 s" >&2; exit 1; }

server=$("${compose[@]}" ps -q relic-server)
docker update --cpus "${RELIC_CPUS:-2}" "$server" >/dev/null
RELIC_PASSWORD=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$server" | sed -n 's/^RELIC_ADMIN_PASSWORD=//p')
export RELIC_PASSWORD
gateway=$(docker network inspect "${project}_default" -f '{{(index .IPAM.Config 0).Gateway}}')

step "pull (${PODS:-100} pods, ${IMAGES:-1} images, Relic on ${RELIC_CPUS:-2} CPUs, database pool ${RELIC_DATABASE_MAX_CONNECTIONS:-20})"
out=${OUT:-$(mktemp --suffix=.json)}
cd "$root"
go run ./api/adapters/artifactory/compat/load/pullstorm \
  -relic "http://localhost:$RELIC_PORT" -listen ":$upstream_port" -upstream-url "http://$gateway:$upstream_port" \
  -pods "${PODS:-100}" -images "${IMAGES:-1}" -layers "${LAYERS:-5}" -shared-layers "${SHARED_LAYERS:-2}" -layer-size "${LAYER_SIZE:-20MiB}" \
  -upstream-rate "${UPSTREAM_RATE:-50MiB}" -out "$out"
echo "results: $out" >&2
