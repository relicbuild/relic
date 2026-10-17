#!/usr/bin/env bash
# covers: config add, config show
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD not set" >&2
  exit 0
fi

JFROG_CLI_HOME_DIR="$WORK/jfhome"
export JFROG_CLI_HOME_DIR

step "config add with password (encrypted password, refreshable token)"
jf config add token-flow --url "$SCENARIO_URL" --user "$SCENARIO_USER" --password "$SCENARIO_PASSWORD" --interactive=false
jf config show token-flow

step "first command creates a token"
[[ "$(jf rt ping)" == "OK" ]]
jq -e '.servers[0].accessToken | length > 0' "$JFROG_CLI_HOME_DIR/jfrog-cli.conf.v6" >/dev/null
jq -e '.servers[0].artifactoryRefreshToken | length > 0' "$JFROG_CLI_HOME_DIR/jfrog-cli.conf.v6" >/dev/null

step "file operations authenticate with the token"
delete_repo "$REPO"
create_generic_repo "$REPO"
make_tree
jf rt u "src/a/*" "$REPO/token/" --flat
jf rt s "$REPO/token/*" | expect_count 2
delete_repo "$REPO"
