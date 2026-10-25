#!/usr/bin/env bash
# covers: apk
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER or a docker daemon missing" >&2
  exit 0
fi
LOCAL=relic-scenario-alpine-local
REMOTE=relic-scenario-alpine-remote
VIRTUAL=relic-scenario-alpine
for r in "$VIRTUAL" "$LOCAL" "$REMOTE"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"alpine"},
 {"key":"$REMOTE","rclass":"remote","packageType":"alpine","url":"https://dl-cdn.alpinelinux.org/alpine"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"alpine","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
ver=v3.24 arch=$(uname -m)

step "the upstream's index and a package through the remote"
jf rt curl -sf -o upstream.tar.gz "/$REMOTE/$ver/main/$arch/APKINDEX.tar.gz"
pkgver=$(tar -xzOf upstream.tar.gz APKINDEX | awk -v RS= '/\nP:tzdata\n/' | sed -n 's/^V://p')
[[ -n "$pkgver" ]] || { echo "tzdata not in the upstream index" >&2; exit 1; }
jf rt curl -sf -o "tzdata-$pkgver.apk" "/$REMOTE/$ver/main/$arch/tzdata-$pkgver.apk"

step "jf apk upload"
jf apk upload "tzdata-$pkgver.apk" --repo "$LOCAL" --alpine-version 3.24 --arch "$arch"
jf rt s "$LOCAL/$ver/main/$arch/tzdata-$pkgver.apk" | expect_count 1

step "apk add from the local repository, trusting only Relic's key"
key=$(jf rt curl -sf "/api/security/keypair/public/repositories/$LOCAL")
base="${SCENARIO_URL/:\/\//://$SCENARIO_USER:$SCENARIO_PASSWORD@}/artifactory"
docker run --rm --network host -e KEY="$key" -e REPO="$base/$LOCAL/$ver/main" alpine:3.24 sh -ec '
  rm -f /etc/apk/keys/*
  printf "%s\n" "$KEY" >/etc/apk/keys/relic-rsa.rsa.pub
  echo "$REPO" >/etc/apk/repositories
  apk update
  apk add tzdata
  apk info -e tzdata'

step "apk add through the virtual (merged index, dependencies from the remote)"
docker run --rm --network host -e KEY="$key" -e REPO="$base/$VIRTUAL/$ver/main" alpine:3.24 sh -ec '
  rm -f /etc/apk/keys/*
  printf "%s\n" "$KEY" >/etc/apk/keys/relic-rsa.rsa.pub
  echo "$REPO" >/etc/apk/repositories
  apk update
  apk add jq
  jq --version'

for r in "$VIRTUAL" "$LOCAL" "$REMOTE"; do delete_repo "$r"; done
