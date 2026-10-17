#!/usr/bin/env bash
# covers: curation-audit
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v go >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER or go missing" >&2
  exit 0
fi
NPM=relic-scenario-ca-npm
GO=relic-scenario-ca-go
for r in "$NPM" "$GO"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$NPM","rclass":"remote","packageType":"npm","url":"https://registry.npmjs.org"},
 {"key":"$GO","rclass":"remote","packageType":"go","url":"https://proxy.golang.org"}]
JSON
jf rt rc repos.json

step "npm project"
mkdir -p "$WORK/npm-app" && cd "$WORK/npm-app"
printf '{"name":"app","version":"0.0.0","private":true,"dependencies":{"is-number":"7.0.0"}}\n' >package.json
jf npmc --server-id-resolve relic --repo-resolve "$NPM"
JFROG_CLI_LOG_LEVEL=DEBUG jf ca 2>ca-npm.log | tee ca-npm.txt || { cat ca-npm.log >&2; exit 1; }
grep -q "Found 0 blocked packages" ca-npm.txt
grep -q "HEAD request to: .*/is-number-7.0.0.tgz" ca-npm.log

step "Go module through the pass-through"
export GOPATH="$WORK/gopath" GOCACHE="$WORK/gocache" GOFLAGS=-modcacherw GOTOOLCHAIN=local
mkdir -p "$WORK/go-app" && cd "$WORK/go-app"
cat >go.mod <<'MOD'
module example.com/app

go 1.21

require github.com/google/uuid v1.6.0
MOD
printf 'package main\n\nimport "github.com/google/uuid"\n\nfunc main() { _ = uuid.New() }\n' >main.go
jf goc --server-id-resolve relic-tls --repo-resolve "$GO"
JFROG_CLI_LOG_LEVEL=DEBUG jf ca 2>ca-go.log | tee ca-go.txt || { cat ca-go.log >&2; exit 1; }
grep -q "Found 0 blocked packages" ca-go.txt
grep -q "HEAD request to: .*/github.com/google/uuid/@v/v1.6.0.zip" ca-go.log

cd "$WORK"
for r in "$NPM" "$GO"; do delete_repo "$r"; done
