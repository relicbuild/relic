#!/usr/bin/env bash
# covers: conan-config, ruby-config
source "$(dirname "$0")/lib.sh"
RESOLVE=$REPO
DEPLOY=$REPO2
for r in "$RESOLVE" "$DEPLOY"; do delete_repo "$r"; done
create_generic_repo "$RESOLVE"
create_generic_repo "$DEPLOY"

for kind in conan ruby; do
  step "jf $kind-config"
  jf "$kind-config" --server-id-resolve relic --repo-resolve "$RESOLVE" \
    --server-id-deploy relic --repo-deploy "$DEPLOY"
  file=".jfrog/projects/$kind.yaml"
  grep -qx "type: $kind" "$file"
  [[ "$(grep -A2 '^resolver:' "$file" | grep -c -e "repo: $RESOLVE" -e 'serverId: relic')" == 2 ]]
  [[ "$(grep -A2 '^deployer:' "$file" | grep -c -e "repo: $DEPLOY" -e 'serverId: relic')" == 2 ]]
done

step "the configured repositories are Relic's"
jf rt curl -s "/api/repositories/$RESOLVE" | jq -e --arg k "$RESOLVE" '.key == $k' >/dev/null
jf rt curl -s "/api/repositories/$DEPLOY" | jq -e --arg k "$DEPLOY" '.key == $k' >/dev/null

for r in "$RESOLVE" "$DEPLOY"; do delete_repo "$r"; done
