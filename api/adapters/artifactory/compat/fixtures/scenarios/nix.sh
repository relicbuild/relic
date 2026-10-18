#!/usr/bin/env bash
# covers: nix
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${TLS_CA:-}" || -z "${SCENARIO_USER:-}" ]] || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_TLS_URL/TLS_CA/SCENARIO_USER or a docker daemon missing" >&2
  exit 0
fi
LOCAL=relic-scenario-nix-local
REMOTE=relic-scenario-nix-remote
VIRTUAL=relic-scenario-nix
for r in "$VIRTUAL" "$LOCAL" "$REMOTE"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"nix"},
 {"key":"$REMOTE","rclass":"remote","packageType":"nix","url":"https://cache.nixos.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"nix","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
host=${SCENARIO_TLS_URL#*://}
host=${host%%/*}
box=relic-scenario-nix-$RANDOM
trap 'docker rm -f "$box" >/dev/null 2>&1 || true; for r in "$VIRTUAL" "$LOCAL" "$REMOTE"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
docker create --name "$box" --network host -e U="$SCENARIO_USER" -e P="$SCENARIO_PASSWORD" -e HOST="$host" \
  -e API="$SCENARIO_TLS_URL/artifactory/api/nix" -e URL="$SCENARIO_TLS_URL" -e LOCAL="$LOCAL" -e REMOTE="$REMOTE" -e VIRTUAL="$VIRTUAL" \
  -e B="$BUILD-nix" nixos/nix:2.35.3 sleep 900 >/dev/null
docker cp "$JF" "$box:/tmp/jf"
docker cp "$TLS_CA" "$box:/tmp/relic-ca.crt"
docker start "$box" >/dev/null

step "jf nix copy of a built path, nix copy --from through the virtual"
docker exec "$box" sh -ec '
  printf "experimental-features = nix-command\nsandbox = false\nsubstituters =\nrequire-sigs = false\nnetrc-file = /root/.netrc\nssl-cert-file = /tmp/relic-ca.crt\n" >>/etc/nix/nix.conf
  mkdir -p /root/.jfrog/security/certs && cp /tmp/relic-ca.crt /root/.jfrog/security/certs/
  printf "machine %s\nlogin %s\npassword %s\n" "${HOST%%:*}" "$U" "$P" >/root/.netrc
  export CI=true JFROG_CLI_REPORT_USAGE=false
  jf() { /tmp/jf "$@"; }
  jf config add relic --url "$URL" --user "$U" --password "$P" --interactive=false >/dev/null
  nix-build -o /tmp/result -E "derivation { name = \"relic-demo\"; system = builtins.currentSystem; builder = \"/bin/sh\"; args = [ \"-c\" \"echo hello-from-relic-$RANDOM > \$out\" ]; }"
  out=$(readlink /tmp/result)
  want=$(cat "$out")
  jf nix copy --to "$API/$LOCAL" "$out" --build-name "$B" --build-number 1
  jf rt bp "$B" 1
  rm /tmp/result
  nix-store --delete "$out"
  test ! -e "$out"
  nix copy --from "$API/$VIRTUAL" "$out"
  test "$(cat "$out")" = "$want"
  echo "restored $out"'
jf rt s "$LOCAL/binary-cache/" | jq -e 'length >= 2' >/dev/null

step "a cache.nixos.org path through the remote"
docker exec "$box" sh -ec '
  bash=$(readlink -f /bin/sh); bash=${bash%/bin/bash}
  nix path-info --store "$API/$REMOTE" "$bash"
  nix store cat --store "$API/$REMOTE" "$bash/bin/bash" >/tmp/bash
  head -c 4 /tmp/bash | od -c | grep -q E'
