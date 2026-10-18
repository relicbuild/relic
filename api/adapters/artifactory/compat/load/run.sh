#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/../../../../.." && pwd)
export RELIC_PORT=28091 RELIC_POSTGRESQL_PORT=15433 RELIC_OBJECT_STORAGE_PORT=18334 RELIC_GARBAGE_COLLECTION_INTERVAL=15s RELIC_GARBAGE_COLLECTION_GRACE_PERIOD=20s RELIC_TRASH_ENABLED=false
compose=(docker compose -p relic-load -f "$root/deploy/docker-compose.yml" -f "$root/test/compose/plain-http.yml")
cleanup() { "${compose[@]}" down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT
password=${RELIC_ADMIN_PASSWORD:-password}
step() { printf '\n==> %s\n' "$*" >&2; }

step "start the stack"
"${compose[@]}" up -d --build --wait relic-server relic-worker >/dev/null
for _ in $(seq 60); do curl -fsS "localhost:$RELIC_PORT/readyz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "localhost:$RELIC_PORT/readyz" >/dev/null 2>&1 || { echo "relic-server not ready after 60 s" >&2; exit 1; }

step "k6 (${DURATION:-2m})"
docker run --rm --network host -v "$root/api/adapters/artifactory/compat/load:/load:ro" -e RELIC_URL="http://localhost:$RELIC_PORT" -e RELIC_PASSWORD="$password" -e DURATION="${DURATION:-2m}" \
  grafana/k6:2.3.0 run --quiet --summary-trend-stats "avg,p(95),p(99),max" /load/relic.js

step "let GC catch up (grace 20 s, interval 15 s)"
sleep 60
psql() { "${compose[@]}" exec -T postgresql psql -U relic -d relic -At -c "$1"; }

step "invariant 1: refcounts match references"
bad=$(psql "SELECT count(*) FROM (SELECT b.sha256, b.refcount, count(i.id) AS refs FROM blobs b LEFT JOIN items i ON i.sha256 = b.sha256 GROUP BY b.sha256, b.refcount) x WHERE refcount <> refs")
echo "blobs with a wrong refcount: $bad" >&2
test "$bad" -eq 0

step "invariant 2: GC kept up"
stale=$(psql "SELECT count(*) FROM blobs WHERE refcount = 0 AND unreferenced_since < now() - interval '50 seconds'")
collected=$("${compose[@]}" logs relic-worker 2>/dev/null | grep -c '"collected blobs"' || true)
echo "stale unreferenced blobs: $stale; GC runs that collected: $collected" >&2
test "$stale" -eq 0
test "$collected" -gt 0

step "invariant 3: every stored file is intact"
items=$(mktemp)
psql "SELECT path || '/' || name || ' ' || sha256 FROM items WHERE repo_key = 'load-local' AND type = 'file'" >"$items"
total=$(wc -l <"$items")
fail=$(while read -r p sha; do
  got=$(curl -fsS -u "admin:$password" "localhost:$RELIC_PORT/artifactory/load-local/$p" | sha256sum | cut -d' ' -f1)
  [[ "$got" == "$sha" ]] || echo "$p"
done <"$items" | wc -l)
rm -f "$items"
echo "files checked: $total, corrupt or missing: $fail" >&2
test "$fail" -eq 0
echo "load test passed"
