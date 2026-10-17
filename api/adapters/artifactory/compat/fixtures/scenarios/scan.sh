#!/usr/bin/env bash
# covers: scan, docker scan
# tier: platform (indexing and matching run in the separate worker)
source "$(dirname "$0")/lib.sh"

step "jf scan finds jackson-databind 2.9.8's advisories"
curl -sfLO https://repo1.maven.org/maven2/com/fasterxml/jackson/core/jackson-databind/2.9.8/jackson-databind-2.9.8.jar
jf scan jackson-databind-2.9.8.jar --format json >scan.json
jq -e '[.. | objects | select(has("components")) | .components | keys[]] | index("gav://com.fasterxml.jackson.core:jackson-databind:2.9.8") != null' scan.json >/dev/null
test -x "${JFROG_CLI_HOME_DIR:-$HOME/.jfrog}/dependencies/xray-indexer/3.131.18/indexer-app"

if docker info >/dev/null 2>&1; then
  step "jf docker scan finds OS package advisories in an old Alpine image"
  docker pull -q alpine:3.17.0 >/dev/null
  jf docker scan alpine:3.17.0 --format json >docker.json
  jq -e '[.. | objects | select(has("components")) | .components | keys[] | select(startswith("alpine://"))] | length >= 1' docker.json >/dev/null
  docker rmi alpine:3.17.0 >/dev/null 2>&1 || true
fi
