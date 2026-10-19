#!/usr/bin/env bash
# Runs one scenario in its own home, jf configuration and temporary
# directory, writing its output to /results/logs/<name>.log. A scenario
# with its own Docker daemon (E2E_OWN_DAEMON) talks to that daemon.
set -euo pipefail
name="${1:?scenario name}"
timeout="${SCENARIO_TIMEOUT:-20m}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
export HOME="$work/home" TMPDIR="$work/tmp" SCENARIO_NAME="$name" SCENARIO_SUFFIX="-$name"
export JFROG_CLI_HOME_DIR="$HOME/.jfrog"
mkdir -p "$HOME" "$TMPDIR" /results/logs
cp -R /root/.jfrog "$JFROG_CLI_HOME_DIR"
if [[ " ${E2E_OWN_DAEMON:-} " == *" $name "* ]]; then
  export DOCKER_HOST="tcp://relic-e2e-dind-$name:2375" SCENARIO_DOCKER_HOSTNAME="relic-e2e-dind-$name"
fi
export SCENARIO_URL="$RELIC_URL" SCENARIO_USER="${RELIC_USER:-admin}" SCENARIO_PASSWORD="${RELIC_PASSWORD:-password}"

cd "$work"
# timeout runs the scenario in its own process group and kills all of it.
exec timeout --kill-after=10s "$timeout" "/scenarios/$name.sh" >"/results/logs/$name.log" 2>&1
