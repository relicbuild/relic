#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/../../../../.." && pwd)
export RELIC_PORT=28092 RELIC_POSTGRESQL_PORT=15434 RELIC_OBJECT_STORAGE_PORT=18335 \
  RELIC_GARBAGE_COLLECTION_INTERVAL=15s RELIC_GARBAGE_COLLECTION_GRACE_PERIOD=20s RELIC_TRASH_ENABLED=false
project=relic-bandwidth
compose=(docker compose -p "$project" -f "$root/deploy/docker-compose.yml" -f "$root/test/compose/plain-http.yml")
cleanup() { "${compose[@]}" down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT
step() { printf '\n==> %s\n' "$*" >&2; }
ready() {
  for _ in $(seq 60); do curl -fsS "localhost:$RELIC_PORT/readyz" >/dev/null 2>&1 && return; sleep 1; done
  echo "relic-server not ready after 60 s" >&2
  return 1
}

step "start the stack"
"${compose[@]}" up -d --build --wait relic-server relic-worker >/dev/null
ready

server=$("${compose[@]}" ps -q relic-server)
if [ -n "${RELIC_MEMORY:-}" ]; then
  docker update --memory "$RELIC_MEMORY" --memory-swap "$RELIC_MEMORY" "$server" >/dev/null
  docker restart "$server" >/dev/null
  ready
fi
env_of() { docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$server" | sed -n "s/^$1=//p"; }
RELIC_PASSWORD=$(env_of RELIC_ADMIN_PASSWORD)
RELIC_OBJECT_STORAGE_ACCESS_KEY=$(env_of RELIC_OBJECT_STORAGE_ACCESS_KEY)
RELIC_OBJECT_STORAGE_SECRET_KEY=$(env_of RELIC_OBJECT_STORAGE_SECRET_KEY)
export RELIC_PASSWORD RELIC_OBJECT_STORAGE_ACCESS_KEY RELIC_OBJECT_STORAGE_SECRET_KEY
containers=$("${compose[@]}" ps --format '{{.Name}}' | grep -E 'relic-server|seaweedfs-(s3|filer|volume)' | paste -sd, -)

step "measure (${STEP:-15s} a step)"
out=${OUT:-$(mktemp --suffix=.json)}
cd "$root"
status=0
go run ./api/adapters/artifactory/compat/load/bandwidth \
  -relic "http://localhost:$RELIC_PORT" -s3 "http://localhost:$RELIC_OBJECT_STORAGE_PORT" \
  -step "${STEP:-15s}" \
  -download-sizes "${DOWNLOAD_SIZES:-10MiB,100MiB,1GiB}" -download-concurrency "${DOWNLOAD_CONCURRENCY:-1,8,32,128,256}" \
  -upload-sizes "${UPLOAD_SIZES:-10MiB,100MiB}" -upload-concurrency "${UPLOAD_CONCURRENCY:-1,8,32}" \
  -containers "$containers" -out "$out" || status=$?
docker logs "$server" 2>&1 | grep -o '"slots":[0-9]*' | tail -1 | sed 's/^/relic-server: upload /' >&2 || true
docker inspect -f 'relic-server: OOM-killed {{.State.OOMKilled}}, running {{.State.Running}}, exit code {{.State.ExitCode}}' "$server" >&2
echo "results: $out" >&2
exit "$status"
