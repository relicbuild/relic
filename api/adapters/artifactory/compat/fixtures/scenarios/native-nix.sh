#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${TLS_CA:-}" || -z "${SCENARIO_USER:-}" ]] || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_TLS_URL/TLS_CA/SCENARIO_USER or a docker daemon missing" >&2
  exit 0
fi
LOCAL=relic-scenario-nnix-local
REMOTE=relic-scenario-nnix-remote
VIRTUAL=relic-scenario-nnix
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"nix"},
 {"key":"$REMOTE","rclass":"remote","packageType":"nix","url":"https://cache.nixos.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"nix","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
host=${SCENARIO_TLS_URL#*://}
host=${host%%/*}
box=relic-scenario-nnix-$RANDOM
trap 'docker rm -f "$box" >/dev/null 2>&1 || true; for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
docker create --name "$box" --network host -e U="$SCENARIO_USER" -e P="$SCENARIO_PASSWORD" -e HOST="$host" \
  -e API="$SCENARIO_TLS_URL/nix" -e LOCAL="$LOCAL" -e REMOTE="$REMOTE" -e VIRTUAL="$VIRTUAL" \
  nixos/nix:2.35.2 sleep infinity >/dev/null
docker cp "$TLS_CA" "$box:/tmp/relic-ca.crt"
docker start "$box" >/dev/null
docker exec "$box" sh -ec '
  printf "experimental-features = nix-command\nsandbox = false\nsubstituters =\nssl-cert-file = /tmp/relic-ca.crt\n" >>/etc/nix/nix.conf'

step "anonymous requests are refused"
docker exec "$box" sh -ec '
  code=$(curl -s -o /dev/null -w "%{http_code}" --cacert /tmp/relic-ca.crt "$API/$VIRTUAL/nix-cache-info")
  test "$code" = 401'

step "netrc credentials; nix copy --to the local, then --from the virtual"
docker exec "$box" sh -ec '
  printf "machine %s\nlogin %s\npassword %s\n" "$HOST" "$U" "$P" >/root/.netrc
  printf "netrc-file = /root/.netrc\n" >>/etc/nix/nix.conf
  nix store info --store "$API/$VIRTUAL" 2>&1 | grep -q "Store URL" # reported on stderr
  nix-build -o /tmp/result -E "derivation { name = \"relic-native\"; system = builtins.currentSystem; builder = \"/bin/sh\"; args = [ \"-c\" \"echo native-$RANDOM > \$out\" ]; }"
  out=$(readlink /tmp/result)
  want=$(cat "$out")
  nix copy --to "$API/$LOCAL" "$out"
  nix path-info --store "$API/$VIRTUAL" "$out"
  rm /tmp/result
  nix-store --delete "$out"
  nix copy --option require-sigs false --from "$API/$VIRTUAL" "$out"
  test "$(cat "$out")" = "$want"'
jf rt s "$LOCAL/binary-cache/" | jq -e 'length >= 2' >/dev/null

step "a cache.nixos.org path through the remote, with signatures checked"
docker exec "$box" sh -ec '
  # The container bash: a cache.nixos.org path with a small closure.
  bash=$(readlink -f /bin/sh); bash=${bash%/bin/bash}
  nix path-info --sigs --store "$API/$VIRTUAL" "$bash" | grep -q "cache.nixos.org-1:"
  nix store verify --store "$API/$VIRTUAL" --sigs-needed 1 \
    --option trusted-public-keys "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=" "$bash"'
jf rt s "$REMOTE/*.narinfo" | jq -e 'length >= 1' >/dev/null
