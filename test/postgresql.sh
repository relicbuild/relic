#!/usr/bin/env bash
# Starts the PostgreSQL server the integration test packages share, or
# reuses a running one, and prints its address for RELIC_TEST_POSTGRESQL_URL.
# Every package then copies its databases from one migrated template instead
# of starting and migrating a server of its own.
set -euo pipefail
name=relic-test-postgresql
image=postgres:18-alpine
if [[ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != true ]]; then
  docker rm -f -v "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" -e POSTGRES_USER=relic -e POSTGRES_PASSWORD=relic -e POSTGRES_DB=relic \
    -p 127.0.0.1::5432 "$image" -c max_connections=2000 >/dev/null
fi
# Over TCP: the server the image runs while it initializes listens only on its socket.
until docker exec "$name" pg_isready -h 127.0.0.1 -U relic -d relic >/dev/null 2>&1; do sleep 1; done
port="$(docker port "$name" 5432/tcp | head -n1 | sed 's/.*://')"
echo "postgres://relic:relic@127.0.0.1:$port/relic?sslmode=disable"
