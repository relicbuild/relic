#!/usr/bin/env bash
# covers: apt, apt-get
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v apt-get >/dev/null || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER, apt-get or a docker daemon missing" >&2
  exit 0
fi
LOCAL=relic-scenario-debian-local
REMOTE=relic-scenario-debian-remote
VIRTUAL=relic-scenario-debian
for r in "$VIRTUAL" "$LOCAL" "$REMOTE"; do delete_repo "$r"; done
arch=$(dpkg --print-architecture)
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"debian"},
 {"key":"$REMOTE","rclass":"remote","packageType":"debian","url":"https://deb.debian.org/debian"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"debian","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL","debianArchitectures":"$arch"}]
JSON
jf rt rc repos.json

step "a real package through the remote"
jf rt curl -sf -o Packages.xz "/$REMOTE/dists/trixie/main/binary-$arch/Packages.xz"
file=$(python3 -c '
import lzma
for p in lzma.open("Packages.xz").read().decode().split("\n\n"):
    f = dict(l.split(": ", 1) for l in p.splitlines() if ": " in l and not l.startswith(" "))
    if f.get("Package") == "hello":
        print(f["Filename"]); break')
[[ -n "$file" ]] || { echo "hello not in the upstream index" >&2; exit 1; }
jf rt curl -sf -o hello.deb "/$REMOTE/$file"

step "jf rt upload --deb"
jf rt u hello.deb "$LOCAL/pool/main/h/hello/" --deb "trixie/main/$arch"
jf rt s "$LOCAL/pool/main/h/hello/hello.deb" | expect_count 1

step "jf apt install, trusting only Relic's key"
jf rt curl -sf "/api/security/keypair/public/repositories/$LOCAL" >/etc/apt/trusted.gpg.d/relic-scenario.asc
jf apt install -y hello --repo "$LOCAL" --dist trixie
hello --version | head -1
rm -f /etc/apt/trusted.gpg.d/relic-scenario.asc

step "apt-get through the virtual (merged index, dependencies from the remote)"
key=$(jf rt curl -sf "/api/security/keypair/public/repositories/$VIRTUAL")
base="${SCENARIO_URL/:\/\//://$SCENARIO_USER:$SCENARIO_PASSWORD@}/artifactory"
# trixie-slim has no CA certificates; an https server is checked against the client's.
docker run --rm -i --network host -e KEY="$key" -e REPO="$base/$VIRTUAL" debian:trixie-slim sh -ec '
  cat >/etc/apt/ca-certificates.crt
  echo "Acquire::https::CAInfo \"/etc/apt/ca-certificates.crt\";" >/etc/apt/apt.conf.d/99ca
  rm -f /etc/apt/sources.list.d/* /etc/apt/sources.list
  mkdir -p /etc/apt/keyrings && printf "%s\n" "$KEY" >/etc/apt/keyrings/relic.asc
  echo "deb [signed-by=/etc/apt/keyrings/relic.asc] $REPO trixie main" >/etc/apt/sources.list.d/relic.list
  apt-get update
  apt-get install -y --no-install-recommends jq
  jq --version' </etc/ssl/certs/ca-certificates.crt

for r in "$VIRTUAL" "$LOCAL" "$REMOTE"; do delete_repo "$r"; done
