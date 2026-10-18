#!/usr/bin/env bash
# covers: docker, docker push, docker pull, rt docker-promote, rt build-docker-create
# tier: platform (image layers stream to S3)
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_REGISTRY:-}" || -z "${SCENARIO_USER:-}" ]] || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_REGISTRY/SCENARIO_USER or a docker daemon missing" >&2
  exit 0
fi
LOCAL=relic-scenario-docker-local
PROD=relic-scenario-docker-prod
HUB=relic-scenario-dockerhub
VIRTUAL=relic-scenario-docker
ALL=("$VIRTUAL" "$LOCAL" "$PROD" "$HUB")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"docker"},
 {"key":"$PROD","rclass":"local","packageType":"docker"},
 {"key":"$HUB","rclass":"remote","packageType":"docker","url":"https://registry-1.docker.io"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"docker","repositories":"$LOCAL,$HUB","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
reg=$SCENARIO_REGISTRY
image="$reg/$VIRTUAL/relic/app"

step "docker login and build"
echo "$SCENARIO_PASSWORD" | docker login "$reg" -u "$SCENARIO_USER" --password-stdin
mkdir ctx && printf 'relic %s\n' "$RANDOM" >ctx/hello.txt
printf 'FROM scratch\nCOPY hello.txt /\n' >ctx/Dockerfile
docker build -q -t "$image:1.0" ctx

step "jf docker push with build info"
jf docker push "$image:1.0" --build-name "$BUILD-docker" --build-number 1
jf rt bp "$BUILD-docker" 1
jf rt s "$LOCAL/relic/app/1.0/manifest.json" | expect_count 1
jf rt curl -s "/api/build/$BUILD-docker/1" | jq -e '.buildInfo.modules[0].artifacts | length >= 3' >/dev/null
jf rt s --build "$BUILD-docker/1" "$LOCAL/" | expect_count 3

step "jf docker pull with build info"
docker rmi "$image:1.0" >/dev/null
jf docker pull "$image:1.0" --build-name "$BUILD-docker-pull" --build-number 1
jf rt bp "$BUILD-docker-pull" 1
jf rt curl -s "/api/build/$BUILD-docker-pull/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 2' >/dev/null

step "jf rt build-docker-create from an image file"
digest=$(jf rt curl -sfI "/api/docker/$LOCAL/v2/relic/app/manifests/1.0" | tr -d '\r' | awk -F': ' 'tolower($1)=="docker-content-digest"{print $2}')
[[ -n "$digest" ]]
echo "$image:1.0@$digest" >image-file.txt
jf rt bdc "$VIRTUAL" --image-file image-file.txt --build-name "$BUILD-docker-bdc" --build-number 1
jf rt bp "$BUILD-docker-bdc" 1
jf rt curl -s "/api/build/$BUILD-docker-bdc/1" | jq -e '.buildInfo.modules[0].artifacts | length >= 3' >/dev/null

step "jf rt docker-promote copies the tag"
jf rt dpr relic/app "$LOCAL" "$PROD" --source-tag 1.0 --target-tag stable --copy
docker pull -q "$reg/$PROD/relic/app:stable" >/dev/null
docker run --rm "$reg/$PROD/relic/app:stable" 2>/dev/null || true # scratch has no shell; pulling is the check

step "jf docker pull of a multi-arch Docker Hub image through the virtual"
jf docker pull "$reg/$VIRTUAL/busybox:1.38" --build-name "$BUILD-docker-hub" --build-number 1
jf rt bp "$BUILD-docker-hub" 1
jf rt curl -s "/api/build/$BUILD-docker-hub/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 2' >/dev/null
jf rt s "$HUB/library/busybox/1.38/list.manifest.json" | expect_count 1

docker rmi "$image:1.0" "$reg/$PROD/relic/app:stable" "$reg/$VIRTUAL/busybox:1.38" >/dev/null 2>&1 || true
for r in "${ALL[@]}"; do delete_repo "$r"; done
