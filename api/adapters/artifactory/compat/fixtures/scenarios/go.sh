#!/usr/bin/env bash
# covers: go, go-config, go-publish
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v go >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER or go missing" >&2
  exit 0
fi
LOCAL=relic-scenario-go-local
REMOTE=relic-scenario-go-remote
VIRTUAL=relic-scenario-go
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"go"},
 {"key":"$REMOTE","rclass":"remote","packageType":"go","url":"https://proxy.golang.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"go","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json

export GOPATH="$WORK/gopath" GOCACHE="$WORK/gocache" GOFLAGS=-modcacherw GONOSUMDB=relic.test GOTOOLCHAIN=local
proxy="${SCENARIO_TLS_URL/:\/\//://$SCENARIO_USER:$SCENARIO_PASSWORD@}/artifactory/api/go/$VIRTUAL"

step "jf go-publish a module to the local repository"
mkdir -p lib && cd lib
printf 'module relic.test/lib\n\ngo 1.21\n' >go.mod
printf 'package lib\n\nfunc Hello() string { return "relic" }\n' >lib.go
jf goc --server-id-resolve relic --repo-resolve "$VIRTUAL" --server-id-deploy relic --repo-deploy "$LOCAL"
jf gp v1.0.0 --build-name "$BUILD-go" --build-number 1
jf rt bp "$BUILD-go" 1
cd ..
jf rt s "$LOCAL/relic.test/lib/@v/v1.0.0.*" | expect_count 3

step "go command resolves the published module and one from proxy.golang.org through the virtual"
mkdir -p app && cd app
printf 'module relic.test/app\n\ngo 1.21\n' >go.mod
cat >main.go <<'GO'
package main

import (
	"fmt"

	"github.com/google/uuid"
	"relic.test/lib"
)

func main() { fmt.Println(lib.Hello(), uuid.NewString() != "") }
GO
GOPROXY="$proxy" go get relic.test/lib@v1.0.0 github.com/google/uuid@v1.6.0
GOPROXY="$proxy" go build ./...
jf rt s "$REMOTE/github.com/google/uuid/@v/v1.6.0.zip" | expect_count 1

step "jf go build with build info"
rm -rf "$GOPATH/pkg/mod"
jf goc --server-id-resolve relic-tls --repo-resolve "$VIRTUAL"
jf go build ./... --build-name "$BUILD-go-app" --build-number 1
jf rt bp "$BUILD-go-app" 1
jf rt curl -s "/api/build/$BUILD-go-app/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 2' >/dev/null
cd ..

for r in "${ALL[@]}"; do delete_repo "$r"; done
