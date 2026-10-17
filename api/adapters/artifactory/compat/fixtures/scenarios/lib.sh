# shellcheck shell=bash
set -euo pipefail

: "${JF:?JF must point to the pinned jf binary (api/adapters/artifactory/compat/jf.sh)}"
export CI=true JFROG_CLI_REPORT_USAGE=false JFROG_CLI_AI_HELP=false

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

# The e2e harness runs scenarios in parallel and sets SCENARIO_SUFFIX (the
# scenario's name) so each one gets its own repositories and builds.
# shellcheck disable=SC2034 # used by the scenarios that source this file
REPO=relic-scenario-generic-local${SCENARIO_SUFFIX:-}
# shellcheck disable=SC2034
REPO2=relic-scenario-generic-target${SCENARIO_SUFFIX:-}
# shellcheck disable=SC2034
BUILD=relic-scenario-build${SCENARIO_SUFFIX:-}

jf() { "$JF" "$@"; }

# The flags registry clients need for SCENARIO_REGISTRY: none when it serves
# HTTPS (SCENARIO_REGISTRY_TLS=true), plain HTTP otherwise.
# shellcheck disable=SC2034
if [[ ${SCENARIO_REGISTRY_TLS:-} == true ]]; then
  REGISTRY_INSECURE=() REGISTRY_PLAIN_HTTP=()
else
  REGISTRY_INSECURE=(--insecure) REGISTRY_PLAIN_HTTP=(--plain-http)
fi

step() { printf '\n==> [%ss] %s\n' "$SECONDS" "$*" >&2; }

create_generic_repo() {
  # shellcheck disable=SC2016
  printf '{"key":"${key}","rclass":"local","packageType":"generic"}\n' >"$WORK/repo.json"
  jf rt repo-create "$WORK/repo.json" --vars "key=$1"
}

delete_repo() {
  jf rt repo-delete "$1" --quiet >/dev/null 2>&1 || true
}

# make_tree creates a small deterministic file tree under ./src.
make_tree() {
  mkdir -p src/a/b src/c
  printf 'one\n' >src/a/one.txt
  printf 'two\n' >src/a/b/two.txt
  printf 'three\n' >src/c/three.bin
  printf 'readme\n' >src/README.md
}

expect_count() {
  local want=$1 got
  got=$(jq 'length')
  if [[ "$got" != "$want" ]]; then
    echo "expected $want results, got $got" >&2
    return 1
  fi
}
