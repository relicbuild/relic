#!/usr/bin/env bash
# Point RELIC_URL at a Relic of its own, not the e2e stack.
set -euo pipefail

JF_TAG=v2.124.0
check_only=false
if [[ ${1:-} == --check ]]; then
  check_only=true
  shift
fi
[[ $# -gt 0 ]] || { echo "usage: $0 [--check] <suite>..." >&2; exit 2; }
if ! $check_only; then
  : "${RELIC_URL:?RELIC_URL must be the Relic platform URL, with trailing slash}"
  : "${RELIC_TOKEN:?RELIC_TOKEN must be a Relic admin token}"
fi

root="$(cd "$(dirname "$0")/../../../../.." && pwd)"
here="$root/api/adapters/artifactory/compat/upstream"
src="${XDG_CACHE_HOME:-$HOME/.cache}/relic/jfrog-cli-$JF_TAG"
results="${RELIC_UPSTREAM_RESULTS:-$here/results}"
mkdir -p "$results"

threshold() {
  awk -v suite="$1" '{ sub(/ *#.*/, "") } $1 == suite ":" { print $2 }' "$here/thresholds.yaml"
}

check() {
  local suite=$1 want pass
  want="$(threshold "$suite")"
  pass="$(jq -r .pass "$results/$suite.json")"
  if [[ -z $want ]]; then
    echo "suite $suite: $pass passed, no threshold" >&2
  elif ((pass < want)); then
    echo "suite $suite: $pass passed, fewer than its threshold $want" >&2
    return 1
  else
    echo "suite $suite: $pass passed, threshold $want" >&2
  fi
}

if $check_only; then
  status=0
  for suite in "$@"; do check "$suite" || status=1; done
  exit "$status"
fi

if [[ ! -d "$src/.git" ]]; then
  git clone -q --depth 1 --branch "$JF_TAG" https://github.com/jfrog/jfrog-cli.git "$src"
fi

skip_regex() {
  awk -v suite="$1" '
    /^[a-zA-Z]/ { in_suite = ($0 == suite ":") }
    in_suite && /^ *- test:/ { sub(/^ *- test: */, ""); tests = tests (tests ? "|" : "") $0 }
    END { if (tests) printf "^(%s)$", tests }
  ' "$here/skips.yaml"
}

registry="${RELIC_URL#*://}"
registry="${registry%%/*}"

gocache="$(go env GOCACHE)" gomodcache="$(go env GOMODCACHE)" gopath="$(go env GOPATH)"
home="$(mktemp -d)"
trap 'rm -rf "$home"' EXIT
cat >"$home/relic_token_test.go" <<'GO'
package main

import (
	"flag"
	"os"
)

func init() {
	_ = flag.Set("jfrog.adminToken", os.Getenv("RELIC_TOKEN"))
}
GO
jq -n --arg file "$src/relic_token_test.go" --arg from "$home/relic_token_test.go" '{Replace: {($file): $from}}' >"$home/overlay.json"

status=0
for suite in "$@"; do
  skip="$(skip_regex "$suite")"
  echo "== suite $suite${skip:+ (skip $skip)}" >&2
  (
    cd "$src"
    export HOME="$home" XDG_CONFIG_HOME="$home/.config" GOCACHE="$gocache" GOMODCACHE="$gomodcache" GOPATH="$gopath"
    go test -json -timeout 2h -overlay "$home/overlay.json" . "-test.$suite" \
      "-jfrog.url=$RELIC_URL" -ci.runId="relic-$suite" \
      "-test.containerRegistry=$registry" \
      ${skip:+-skip "$skip"}
  ) >"$results/$suite.jsonl" || true
  jq -s --arg suite "$suite" '
    map(select(.Test != null and (.Action == "pass" or .Action == "fail" or .Action == "skip")))
    | { suite: $suite,
        pass: map(select(.Action == "pass")) | length,
        fail: map(select(.Action == "fail")) | length,
        skip: map(select(.Action == "skip")) | length }
  ' "$results/$suite.jsonl" | tee "$results/$suite.json"
  check "$suite" || status=1
done
exit "$status"
