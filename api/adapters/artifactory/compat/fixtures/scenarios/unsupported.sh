#!/usr/bin/env bash
# covers: release-bundle-distribute, malicious-scan
source "$(dirname "$0")/lib.sh"

fails_with() {
  local want=$1 out
  shift
  if out=$(jf "$@" 2>&1); then
    echo "jf $* succeeded; expected a clean failure" >&2
    exit 1
  fi
  if ! grep -qiE "$want" <<<"$out"; then
    echo "jf $* failed without saying why ($want):" >&2
    echo "$out" >&2
    exit 1
  fi
  echo "jf $1 failed cleanly: $(grep -iE "$want" <<<"$out" | head -1)" >&2
}

step "jf release-bundle-distribute"
fails_with "does not distribute" release-bundle-distribute relic-scenario-bundle 1.0 --site '*' --create-repo --sync=false

step "jf malicious-scan"
mkdir -p proj && printf '{"name":"p","version":"1.0.0"}\n' >proj/package.json
fails_with "requires version 3\.132\.0" malicious-scan --working-dirs proj
