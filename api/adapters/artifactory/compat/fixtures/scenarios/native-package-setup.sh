#!/usr/bin/env bash
# covers:
# relic package setup --write, then the real clients with only what it wrote
# and printed, from contexts that trust the TLS proxy's authority
# (--ca-file). go, set up from an address with a port, reads its .netrc
# machine host:port; pip, twine and Poetry trust the authority from their
# own configuration, with the system's trust store left out; npm trusts it
# with the printed NODE_EXTRA_CA_CERTS; Poetry, for a repository named
# pypi, keeps its credentials off Poetry's PyPI name.
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" || ! -f "${TLS_CA:-}" ]] ||
  ! command -v relic >/dev/null || ! command -v go >/dev/null || ! command -v npm >/dev/null ||
  ! command -v poetry >/dev/null || ! command -v twine >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER/TLS_CA, relic, go, npm, poetry or twine missing" >&2
  exit 0
fi
GO=relic-scenario-setup-go
NPM=relic-scenario-setup-npm
PYPI=pypi
ALL=("$GO" "$NPM" "$PYPI")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$GO","rclass":"local","packageType":"go"},
 {"key":"$NPM","rclass":"local","packageType":"npm"},
 {"key":"$PYPI","rclass":"local","packageType":"pypi"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
unset RELIC_URL RELIC_TOKEN NODE_USE_SYSTEM_CA
export JFROG_CLI_HOME_DIR="${JFROG_CLI_HOME_DIR:-$HOME/.jfrog}"
export HOME="$WORK/home" RELIC_CONFIG="$WORK/relic.yaml"
export GOPATH="$WORK/gopath" GOCACHE="$WORK/gocache" GOFLAGS=-modcacherw GOTOOLCHAIN=local
unset GOPROXY GONOSUMDB GOPRIVATE GONOPROXY GOINSECURE GOSUMDB
mkdir -p "$HOME"
host=${SCENARIO_TLS_URL#https://}
host=${host%%/*}
server="https://$host:443"
# A trust store without the proxy's authority, for the clients that must
# take it from what setup wrote.
mkdir -p "$WORK/no-ca"
cp /usr/share/ca-certificates/mozilla/ISRG_Root_X1.crt "$WORK/no-ca/bundle.pem"
without_system_trust() { SSL_CERT_FILE="$WORK/no-ca/bundle.pem" SSL_CERT_DIR="$WORK/no-ca" "$@"; }

login() { # login <url> <context>
  printf '%s\n' "$SCENARIO_PASSWORD" |
    relic auth login "$1" --context "$2" --user "$SCENARIO_USER" --password-stdin --ca-file "$TLS_CA" >/dev/null
}

step "go: setup writes a .netrc machine with the port, and go get authenticates with it"
mkdir -p "mod/relic.test/setup@v1.0.0"
printf 'module relic.test/setup\n\ngo 1.21\n' >"mod/relic.test/setup@v1.0.0/go.mod"
printf 'package setup\n\nfunc Hello() string { return "hello from setup" }\n' >"mod/relic.test/setup@v1.0.0/setup.go"
(cd mod && zip -qrD ../v1.0.0.zip "relic.test/setup@v1.0.0")
printf '{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}' >v1.0.0.info
for f in v1.0.0.mod v1.0.0.info v1.0.0.zip; do
  src=$f
  [[ $f == v1.0.0.mod ]] && src="mod/relic.test/setup@v1.0.0/go.mod"
  curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -T "$src" "$SCENARIO_TLS_URL/go/$GO/relic.test/setup/@v/$f" >/dev/null
done
login "$server" ported
relic package setup go "$GO" --private 'relic.test/*' --write
grep -q "^machine $host:443 " "$HOME/.netrc"
go env GOPROXY GONOSUMDB
mkdir goapp && cd goapp
printf 'module relic.test/goapp\n\ngo 1.21\n' >go.mod
printf 'package main\n\nimport (\n\t"fmt"\n\n\t"relic.test/setup"\n)\n\nfunc main() { fmt.Println(setup.Hello()) }\n' >main.go
go get relic.test/setup@v1.0.0
[[ "$(go run .)" == "hello from setup" ]]
cd ..

step "Poetry for a repository named pypi: its own name, the printed source add and publish -r work"
login "$SCENARIO_TLS_URL" plain
relic package setup poetry "$PYPI" --write | tee poetry-setup.txt
grep -q "poetry publish -r relic-pypi" poetry-setup.txt
poetry config repositories.relic-pypi | grep -q "/pypi/$PYPI"
if grep -q 'http-basic.pypi\]' "$HOME/.config/pypoetry/auth.toml" 2>/dev/null ||
  poetry config http-basic.pypi >/dev/null 2>&1; then
  echo "setup stored credentials under Poetry's pypi name" >&2
  exit 1
fi
poetry new -q relic-setup-demo
cd relic-setup-demo
sed -n 's/^For dependencies: //p' ../poetry-setup.txt | sh
grep -q 'name = "relic-pypi"' pyproject.toml
poetry build -q
without_system_trust poetry publish -r relic-pypi
cd ..
jf rt s "$PYPI/*relic_setup_demo-0.1.0*" | jq -e 'length >= 1' >/dev/null

step "twine: setup writes ca_cert to ~/.pypirc; upload trusts it without the system store"
relic package setup twine "$PYPI" --write
grep -q '^ca_cert = ' "$HOME/.pypirc"
(cd relic-setup-demo && poetry version -q 0.2.0 && poetry build -q)
without_system_trust twine upload -r "$PYPI" relic-setup-demo/dist/*0.2.0*

step "pip: setup writes cert to pip.conf; a download trusts it without the system store"
relic package setup pip "$PYPI" --write
python3 -m venv venv
without_system_trust venv/bin/pip download -q --no-deps --no-cache-dir -d dl relic-setup-demo==0.2.0
ls dl | grep -q 'relic_setup_demo-0.2.0'

step "npm: without the printed NODE_EXTRA_CA_CERTS npm refuses the authority; with it, publish and install work"
relic package setup npm "$NPM" --write | tee npm-setup.txt
ca_export=$(grep -o 'export NODE_EXTRA_CA_CERTS=[^ ]*' npm-setup.txt)
mkdir npmpkg && cd npmpkg
printf '{"name":"relic-setup-demo","version":"1.0.0","main":"index.js"}\n' >package.json
printf 'module.exports = "hello from setup";\n' >index.js
if npm publish --fetch-retries=0 >../npm-untrusted.txt 2>&1; then
  echo "npm trusted the authority without NODE_EXTRA_CA_CERTS" >&2
  exit 1
fi
grep -Eq 'SELF_SIGNED|UNABLE_TO|unable to (get|verify)' ../npm-untrusted.txt
eval "$ca_export"
npm publish
cd ..
mkdir npmapp && cd npmapp
npm init -y >/dev/null
npm install relic-setup-demo@1.0.0
[[ "$(node -p 'require("relic-setup-demo")')" == "hello from setup" ]]
cd ..
