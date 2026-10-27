#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_REGISTRY:-}" || -z "${SCENARIO_USER:-}" ]] || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_REGISTRY/SCENARIO_USER or a docker daemon missing" >&2
  exit 0
fi
LOCAL=relic-scenario-ndocker-local
COPY=relic-scenario-ndocker-copy
HUB=relic-scenario-ndocker-hub
VIRTUAL=relic-scenario-ndocker
ALL=("$VIRTUAL" "$LOCAL" "$COPY" "$HUB")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"docker"},
 {"key":"$COPY","rclass":"local","packageType":"docker"},
 {"key":"$HUB","rclass":"remote","packageType":"docker","url":"https://registry-1.docker.io"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"docker","repositories":"$LOCAL,$HUB","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
reg=$SCENARIO_REGISTRY
image="$reg/$LOCAL/native/app"
export DOCKER_CONFIG="$WORK/docker-config"
mkdir -p "$DOCKER_CONFIG"
cleanup() {
  docker rmi -f "$image:1.0" "$reg/$VIRTUAL/native/app:1.0" "$reg/$VIRTUAL/busybox:1.38" \
    "$reg/$LOCAL/native/busybox:amd64" "$reg/$LOCAL/native/busybox:arm64" >/dev/null 2>&1 || true
  for r in "${ALL[@]}"; do delete_repo "$r"; done
}
trap 'cleanup; rm -rf "$WORK"' EXIT

step "anonymous access is refused"
mkdir ctx && printf 'relic native %s\n' "$RANDOM" >ctx/hello.txt
printf 'FROM scratch\nCOPY hello.txt /\n' >ctx/Dockerfile
docker build -q -t "$image:1.0" ctx >/dev/null
if docker push "$image:1.0" >/dev/null 2>&1; then
  echo "anonymous push was accepted" >&2
  exit 1
fi

step "docker login, push, pull through local and virtual"
echo "$SCENARIO_PASSWORD" | docker login "$reg" -u "$SCENARIO_USER" --password-stdin
docker push "$image:1.0"
pushed=$(docker inspect --format '{{index .RepoDigests 0}}' "$image:1.0")
docker rmi "$image:1.0" >/dev/null
docker pull "$image:1.0"
docker pull "$reg/$VIRTUAL/native/app:1.0"
[[ "$(docker inspect --format '{{.Id}}' "$image:1.0")" == "$(docker inspect --format '{{.Id}}' "$reg/$VIRTUAL/native/app:1.0")" ]]
docker pull "$image@${pushed#*@}"

step "docker search finds the pushed image"
docker search "$reg/$LOCAL/native" --format '{{.Name}}' | tee search.txt
grep -q "$LOCAL/native/app" search.txt

step "Docker Hub through the remote, via the virtual"
docker pull "$reg/$VIRTUAL/busybox:1.38"
docker run --rm "$reg/$VIRTUAL/busybox:1.38" echo hello-through-relic | grep -q hello-through-relic

step "multi-arch index with docker manifest"
for arch in amd64 arm64; do
  docker pull -q --platform "linux/$arch" "$reg/$VIRTUAL/busybox:1.38" >/dev/null
  docker tag "$reg/$VIRTUAL/busybox:1.38" "$reg/$LOCAL/native/busybox:$arch"
  docker push -q --platform "linux/$arch" "$reg/$LOCAL/native/busybox:$arch" >/dev/null
done
docker manifest create "${REGISTRY_INSECURE[@]}" "$reg/$LOCAL/native/busybox:multi" \
  "$reg/$LOCAL/native/busybox:amd64" "$reg/$LOCAL/native/busybox:arm64"
docker manifest annotate "$reg/$LOCAL/native/busybox:multi" "$reg/$LOCAL/native/busybox:arm64" --os linux --arch arm64
docker manifest push "${REGISTRY_INSECURE[@]}" --purge "$reg/$LOCAL/native/busybox:multi"
docker manifest inspect "${REGISTRY_INSECURE[@]}" "$reg/$LOCAL/native/busybox:multi" | jq -e '[.manifests[].platform.architecture] | sort == ["amd64","arm64"]' >/dev/null

step "crane: auth, catalog, tags, digest, copy, config, delete"
export CRANE="crane ${REGISTRY_INSECURE[*]}"
crane auth login "$reg" -u "$SCENARIO_USER" -p "$SCENARIO_PASSWORD"
$CRANE catalog "$reg" | grep -x "$LOCAL/native/app" >/dev/null
$CRANE ls "$reg/$LOCAL/native/busybox" | sort | tr '\n' ' ' | grep -q "amd64 arm64 multi"
[[ "$($CRANE digest "$image:1.0")" == "${pushed#*@}" ]]
$CRANE copy "$reg/$LOCAL/native/busybox:multi" "$reg/$COPY/mirrored/busybox:1"
$CRANE manifest "$reg/$COPY/mirrored/busybox:1" | jq -e '.manifests | length == 2' >/dev/null
$CRANE config "$reg/$COPY/mirrored/busybox:1" --platform linux/arm64 | jq -e '.architecture == "arm64"' >/dev/null
$CRANE delete "$reg/$COPY/mirrored/busybox:1"
if $CRANE manifest "$reg/$COPY/mirrored/busybox:1" >/dev/null 2>&1; then
  echo "deleted manifest still served" >&2
  exit 1
fi

step "docker logout ends access"
docker logout "$reg"
docker rmi "$image:1.0" >/dev/null
if docker pull "$image:1.0" >/dev/null 2>&1; then
  echo "pull after logout was accepted" >&2
  exit 1
fi
