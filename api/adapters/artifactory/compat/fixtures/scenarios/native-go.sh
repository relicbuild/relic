#!/usr/bin/env bash
# covers:
# The go command against Relic as its only GOPROXY (no ",direct"), with
# credentials from ~/.netrc and checksum verification left on: a private
# module deployed with curl (GONOSUMDB), public modules through a remote of
# proxy.golang.org, sum.golang.org proxied through Relic, go list -m
# -versions, go get, go mod verify and go install. Uses SCENARIO_TLS_URL
# (the go command sends credentials only over HTTPS).
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v go >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER or go missing" >&2
  exit 0
fi
LOCAL=relic-scenario-ngo-local
REMOTE=relic-scenario-ngo-remote
VIRTUAL=relic-scenario-ngo
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"go"},
 {"key":"$REMOTE","rclass":"remote","packageType":"go","url":"https://proxy.golang.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"go","repositories":"$LOCAL,$REMOTE"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
host=${SCENARIO_TLS_URL#*://}
host=${host%%/*}
export JFROG_CLI_HOME_DIR="${JFROG_CLI_HOME_DIR:-$HOME/.jfrog}"
export HOME="$WORK/home" GOPATH="$WORK/gopath" GOCACHE="$WORK/gocache" GOFLAGS=-modcacherw GOTOOLCHAIN=local
mkdir -p "$HOME"
printf 'machine %s\nlogin %s\npassword %s\n' "$host" "$SCENARIO_USER" "$SCENARIO_PASSWORD" >"$HOME/.netrc"
chmod 600 "$HOME/.netrc"
export GOPROXY="$SCENARIO_TLS_URL/go/$VIRTUAL" GONOSUMDB=relic.test GOSUMDB=sum.golang.org
unset GOPRIVATE GONOPROXY GOINSECURE

step "deploy a private module with curl (.mod, .info, .zip)"
mkdir -p "mod/relic.test/nlib@v1.0.0"
printf 'module relic.test/nlib\n\ngo 1.21\n' >"mod/relic.test/nlib@v1.0.0/go.mod"
printf 'package nlib\n\nfunc Hello() string { return "hello from relic" }\n' >"mod/relic.test/nlib@v1.0.0/nlib.go"
(cd mod && zip -qrD ../v1.0.0.zip "relic.test/nlib@v1.0.0")
printf '{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}' >v1.0.0.info
for f in v1.0.0.mod v1.0.0.info v1.0.0.zip; do
  src=$f
  [[ $f == v1.0.0.mod ]] && src="mod/relic.test/nlib@v1.0.0/go.mod"
  curl -sf --netrc -T "$src" "$SCENARIO_TLS_URL/go/$LOCAL/relic.test/nlib/@v/$f" >/dev/null
done

step "the proxy serves the checksum database"
[[ "$(curl -s --netrc -o /dev/null -w '%{http_code}' "$GOPROXY/sumdb/sum.golang.org/supported")" == 200 ]]

step "go list -m -versions through Relic"
go list -m -versions relic.test/nlib | grep -q "v1.0.0"
go list -m -versions github.com/google/uuid | grep -q "v1.6.0"

step "go get, build and verify, with sum.golang.org through Relic"
mkdir app && cd app
printf 'module relic.test/app\n\ngo 1.21\n' >go.mod
cat >main.go <<'GO'
package main

import (
	"fmt"

	"github.com/google/uuid"
	"relic.test/nlib"
)

func main() { fmt.Println(nlib.Hello(), uuid.NewString() != "") }
GO
go get relic.test/nlib@v1.0.0 github.com/google/uuid@v1.6.0
go build -o app .
./app | grep -q "hello from relic true"
go mod verify
grep -q "github.com/google/uuid v1.6.0 h1:" go.sum
cd ..
jf rt s "$REMOTE/.sumdb/sum.golang.org/lookup/*" | jq -e 'length >= 1' >/dev/null
jf rt s "$REMOTE/.sumdb/sum.golang.org/tile/*" | jq -e 'length >= 1' >/dev/null

step "go install a tool through Relic"
go install golang.org/x/example/hello@latest
"$GOPATH/bin/hello" | grep -qi hello
