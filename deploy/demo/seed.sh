#!/usr/bin/env bash
# Fills the read-only demo: the resources of demo.yaml, then packages from the
# public registries at pinned versions, published with each format's own
# client in its official container image. Safe to run again.
#
#   RELIC_URL              the server, as the relic CLI reaches it
#   RELIC_TOKEN            an administrator's access token
#   RELIC_USER             that administrator's username (default admin)
#   RELIC_SEED_CLIENT_URL  the server as the client containers reach it
#                          (default RELIC_URL)
#   RELIC_SEED_NETWORK     the containers' Docker network (default host)
set -euo pipefail

: "${RELIC_URL:?set RELIC_URL}" "${RELIC_TOKEN:?set RELIC_TOKEN}"
user=${RELIC_USER:-admin}
client_url=${RELIC_SEED_CLIENT_URL:-$RELIC_URL}
client_url=${client_url%/}
registry=${client_url#*://}
network=${RELIC_SEED_NETWORK:-host}
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for tool in relic docker curl tar; do
  command -v "$tool" >/dev/null || { echo "seed: $tool is required" >&2; exit 1; }
done

step() { printf '\n== %s\n' "$*"; }

# client runs a shell command in an image, with the work directory at /work.
client() {
  local image=$1 shell=$2
  shift 2
  docker run --rm --network "$network" -v "$work:/work" -w /work --entrypoint "$shell" \
    -e RELIC_TOKEN -e CLIENT_URL="$client_url" -e REGISTRY="$registry" -e RELIC_USER="$user" \
    "$image" "$@"
}

# stored reports whether repository/path holds a file.
stored() {
  curl -fsS -o /dev/null -I -H "Authorization: Bearer $RELIC_TOKEN" "${RELIC_URL%/}/api/v1/files/$1/$2" 2>/dev/null
}

step "repositories, the demo user, policies and the watch"
relic apply -f "$here/demo.yaml"

step "npm: express 4.17.1 with its dependencies, and lodash 4.17.15"
client node:22-slim bash -euo pipefail -c '
  mkdir -p npm && cd npm
  npm init -y >/dev/null
  npm install --package-lock-only --ignore-scripts --no-audit --no-fund express@4.17.1 lodash@4.17.15 >/dev/null
  node -e "
    const lock = require(\"./package-lock.json\");
    for (const [path, p] of Object.entries(lock.packages)) {
      if (path) console.log(path.split(\"node_modules/\").pop() + \"@\" + p.version);
    }
  " | sort -u > packages.txt
  registry="$CLIENT_URL/npm/npm-local/"
  printf "//%s:_authToken=%s\n" "${registry#*://}" "$RELIC_TOKEN" > .npmrc
  while read -r spec; do
    tarball=$(npm pack "$spec" --silent 2>/dev/null | tail -1)
    if out=$(npm publish "$tarball" --registry "$registry" --userconfig .npmrc --ignore-scripts 2>&1); then
      echo "published $spec"
    elif grep -q -E "E403|E409|EPUBLISHCONFLICT|cannot publish over|already exists" <<<"$out"; then
      echo "present   $spec"
    else
      echo "$out" >&2
      exit 1
    fi
    rm -f "$tarball"
  done < packages.txt
'

step "Maven: log4j-core 2.14.1 and commons-text 1.9, with their parents and dependencies"
central=https://repo1.maven.org/maven2
while read -r group artifact version packaging; do
  dir="${group//.//}/$artifact/$version"
  for ext in pom $([[ $packaging == jar ]] && echo jar); do
    file="$artifact-$version.$ext"
    if stored maven-local "$dir/$file"; then
      echo "present   $dir/$file"
      continue
    fi
    curl -fsSL -o "$work/$file" "$central/$dir/$file"
    relic artifact upload "$work/$file" "maven-local/$dir/$file" >/dev/null
    echo "uploaded  $dir/$file"
  done
done <<'EOF'
org.apache apache 23 pom
org.apache.logging logging-parent 3 pom
org.apache.logging.log4j log4j 2.14.1 pom
org.apache.logging.log4j log4j-api 2.14.1 jar
org.apache.logging.log4j log4j-core 2.14.1 jar
org.apache.commons commons-parent 51 pom
org.apache.commons commons-lang3 3.11 jar
org.apache.commons commons-text 1.9 jar
EOF

step "PyPI: requests 2.25.1 with urllib3 1.26.5 and its other dependencies"
client python:3.12-slim bash -euo pipefail -c '
  pip install --quiet --disable-pip-version-check twine
  pip download --quiet --disable-pip-version-check --only-binary=:all: -d dist requests==2.25.1 urllib3==1.26.5 certifi==2020.12.5 chardet==4.0.0 idna==2.10
  for file in dist/*; do
    if out=$(twine upload --non-interactive --repository-url "$CLIENT_URL/pypi/pypi-local" \
      -u "$RELIC_USER" -p "$RELIC_TOKEN" "$file" 2>&1); then
      echo "published ${file#dist/}"
    elif grep -q -i -E "409|already exists|file exists" <<<"$out"; then
      echo "present   ${file#dist/}"
    else
      echo "$out" >&2
      exit 1
    fi
  done
'

step "Docker: alpine 3.20 and nginx 1.27 for linux/amd64"
insecure=$([[ $client_url == http://* ]] && echo --insecure || true)
for image in alpine:3.20 nginx:1.27; do
  client gcr.io/go-containerregistry/crane:debug sh -c \
    "crane auth login \"\$REGISTRY\" -u \"\$RELIC_USER\" -p \"\$RELIC_TOKEN\" $insecure >/dev/null &&
     crane copy $insecure --platform linux/amd64 docker.io/library/$image \"\$REGISTRY/docker-local/$image\""
  echo "copied    $image"
done

step "Go: relic.build/demo/greeter v1.0.0"
if stored go-local "relic.build/demo/greeter/@v/v1.0.0.info"; then
  echo "present   relic.build/demo/greeter v1.0.0"
else
  relic package publish go go-local v1.0.0 --dir "$here/content/go/greeter"
fi

step "Helm: hello 0.1.0"
tar -czf "$work/hello-0.1.0.tgz" -C "$here/content/helm" hello
relic artifact upload "$work/hello-0.1.0.tgz" helm-local/hello-0.1.0.tgz >/dev/null
echo "uploaded  hello-0.1.0.tgz"

step "generic: an app archive with a planted key and a weak Dockerfile"
mkdir -p "$work/app/config"
cp "$here/content/generic/Dockerfile" "$work/app/Dockerfile"
key=AKIA$(od -An -N16 -tu1 /dev/urandom |
  awk '{ for (i = 1; i <= NF; i++) printf "%s", substr("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", $i % 32 + 1, 1) }')
secret=$(head -c 30 /dev/urandom | base64)
printf 'AWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=%s\n' "$key" "$secret" >"$work/app/config/app.env"
tar -czf "$work/app-1.0.0.tar.gz" -C "$work" app
relic artifact upload "$work/app-1.0.0.tar.gz" generic-local/demo-app/1.0.0/app-1.0.0.tar.gz --build demo-app/1 >/dev/null
relic build add-dependencies demo-app 1 --from-relic --type maven \
  maven-local/org/apache/logging/log4j/log4j-core/2.14.1/log4j-core-2.14.1.jar \
  maven-local/org/apache/commons/commons-text/1.9/commons-text-1.9.jar
relic build publish demo-app 1
echo "published build demo-app 1"

step "a second build run and its release"
relic artifact upload "$work/app-1.0.0.tar.gz" generic-local/demo-app/1.0.1/app-1.0.1.tar.gz --build demo-app/2 >/dev/null
relic build add-dependencies demo-app 2 --from-relic --type maven \
  maven-local/org/apache/commons/commons-text/1.9/commons-text-1.9.jar
relic build publish demo-app 2
if ! relic release get demo-app 1.0.1 >/dev/null 2>&1; then
  relic release create demo-app 1.0.1 --build demo-app/2
fi
echo "released  demo-app 1.0.1"

step "done"
echo "Sign in as demo / demo, or pull with: relic auth login ${RELIC_URL%/}"
