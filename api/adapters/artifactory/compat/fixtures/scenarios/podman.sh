#!/usr/bin/env bash
# covers: rt podman-push, rt podman-pull
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_REGISTRY:-}" || -z "${SCENARIO_USER:-}" ]] || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_REGISTRY/SCENARIO_USER or a docker daemon missing" >&2
  exit 0
fi
LOCAL=relic-scenario-podman-local
delete_repo "$LOCAL"
printf '[{"key":"%s","rclass":"local","packageType":"docker"}]' "$LOCAL" >repos.json
jf rt rc repos.json
box=relic-scenario-podman-$RANDOM
trap 'docker rm -f "$box" >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT
docker create --name "$box" --privileged --network host -e U="$SCENARIO_USER" -e P="$SCENARIO_PASSWORD" \
  -e URL="$SCENARIO_URL" -e REG="$SCENARIO_REGISTRY" -e LOCAL="$LOCAL" -e B="$BUILD-podman" \
  -e STORAGE_DRIVER=vfs quay.io/podman/stable:v5 sleep 900 >/dev/null
docker cp "$JF" "$box:/tmp/jf"
docker start "$box" >/dev/null

step "jf rt podman-push and podman-pull with build info"
docker exec "$box" bash -ec '
  printf "[[registry]]\nlocation = \"%s\"\ninsecure = true\n" "$REG" >>/etc/containers/registries.conf
  export CI=true JFROG_CLI_REPORT_USAGE=false
  jf() { /tmp/jf "$@"; }
  jf config add relic --url "$URL" --user "$U" --password "$P" --interactive=false >/dev/null
  echo "$P" | podman login "$REG" -u "$U" --password-stdin
  image="$REG/$LOCAL/relic/podman-app:1.0"
  mkdir /tmp/ctx && echo "relic $RANDOM" >/tmp/ctx/hello.txt
  printf "FROM scratch\nCOPY hello.txt /\n" >/tmp/ctx/Containerfile
  podman build -q -t "$image" /tmp/ctx
  jf rt podman-push "$image" "$LOCAL" --build-name "$B" --build-number 1
  jf rt bp "$B" 1
  podman rmi "$image" >/dev/null
  jf rt podman-pull "$image" "$LOCAL" --build-name "$B-pull" --build-number 1
  jf rt bp "$B-pull" 1
  podman image exists "$image"'
jf rt s "$LOCAL/relic/podman-app/1.0/manifest.json" | expect_count 1
jf rt curl -s "/api/build/$BUILD-podman/1" | jq -e '.buildInfo.modules[0].artifacts | length >= 2' >/dev/null
jf rt curl -s "/api/build/$BUILD-podman-pull/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 2' >/dev/null

delete_repo "$LOCAL"
