#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v uv >/dev/null || ! command -v poetry >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER, uv or poetry missing" >&2
  exit 0
fi
LOCAL=relic-scenario-npypi-local
REMOTE=relic-scenario-npypi-pypi
VIRTUAL=relic-scenario-npypi
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"pypi"},
 {"key":"$REMOTE","rclass":"remote","packageType":"pypi","url":"https://files.pythonhosted.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"pypi","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done
  jf rt curl -s -X DELETE /api/v2/security/permissions/relic-scenario-npypi-dev >/dev/null 2>&1
  jf rt curl -s -X DELETE /api/security/users/relic-scenario-npypi-dev >/dev/null 2>&1; rm -rf "$WORK"' EXIT
export JFROG_CLI_HOME_DIR="${JFROG_CLI_HOME_DIR:-$HOME/.jfrog}"
export HOME="$WORK/home"
mkdir -p "$HOME"
bundle=/etc/ssl/certs/ca-certificates.crt # holds the e2e CA (run-in-container.sh)
export SSL_CERT_FILE="$bundle" REQUESTS_CA_BUNDLE="$bundle" PIP_DISABLE_PIP_VERSION_CHECK=1 \
  PIP_CACHE_DIR="$WORK/pip-cache" UV_CACHE_DIR="$WORK/uv-cache" POETRY_CACHE_DIR="$WORK/poetry-cache" \
  POETRY_VIRTUALENVS_IN_PROJECT=true
api="$SCENARIO_TLS_URL/pypi"
creds="$SCENARIO_USER:$SCENARIO_PASSWORD"
authed() { echo "${1/:\/\//://$creds@}"; } # authed <url>: the URL with credentials
simple="$api/$VIRTUAL/simple"

step "anonymous installs are refused"
python3 -m venv anon && if anon/bin/pip install -q --index-url "$simple" six==1.16.0 >/dev/null 2>&1; then
  echo "anonymous pip install was accepted" >&2
  exit 1
fi

step "build a package and upload it with twine (~/.pypirc)"
mkdir -p lib/relic_native && cd lib
printf 'def hello():\n    return "hello from relic"\n' >relic_native/__init__.py
cat >pyproject.toml <<TOML
[project]
name = "relic-native"
version = "1.0.0"
description = "Relic native client test"
requires-python = ">=3.9"
dependencies = ["six>=1.16"]

[build-system]
requires = ["poetry-core>=2.0"]
build-backend = "poetry.core.masonry.api"
TOML
poetry build -q
cd ..
cat >"$HOME/.pypirc" <<INI
[distutils]
index-servers = relic

[relic]
repository = $api/$LOCAL
username = $SCENARIO_USER
password = $SCENARIO_PASSWORD
INI
twine upload -r relic lib/dist/*
dev=relic-scenario-npypi-dev
jf rt curl -s -X PUT -H 'Content-Type: application/json' "/api/security/users/$dev" \
  -d "{\"email\":\"dev@example.com\",\"password\":\"Dev-pass-12345\"}" >/dev/null
jf rt curl -s -X POST -H 'Content-Type: application/json' "/api/v2/security/permissions/$dev" \
  -d "{\"name\":\"$dev\",\"repo\":{\"repositories\":[\"$LOCAL\",\"$VIRTUAL\"],\"include-patterns\":[\"**\"],\"actions\":{\"users\":{\"$dev\":[\"read\",\"write\"]}}}}" >/dev/null
if twine upload --repository-url "$api/$LOCAL" -u "$dev" -p Dev-pass-12345 lib/dist/*.whl >/dev/null 2>&1; then
  echo "a deploy-only user overwrote an existing file" >&2
  exit 1
fi
twine upload --skip-existing --repository-url "$api/$LOCAL" -u "$dev" -p Dev-pass-12345 lib/dist/*.whl
jf rt s "$LOCAL/relic-native/1.0.0/*" | expect_count 2

step "pip install through the virtual (local and pypi.org), then hash-checking mode"
python3 -m venv venv
venv/bin/pip install -q --index-url "$(authed "$simple")" relic-native==1.0.0
venv/bin/python -c 'import relic_native, six; print(relic_native.hello())' | grep -q "hello from relic"
jf rt s "$REMOTE/packages/*/six-*-py2.py3-none-any.whl" | jq -e 'length >= 1' >/dev/null
venv/bin/pip download -q --no-deps --index-url "$(authed "$simple")" -d dl six==1.16.0
printf 'six==1.16.0 --hash=sha256:%s\n' "$(sha256sum dl/six-1.16.0-py2.py3-none-any.whl | cut -d' ' -f1)" >requirements.txt
python3 -m venv hashed
hashed/bin/pip install -q --require-hashes --index-url "$(authed "$simple")" -r requirements.txt
printf 'six==1.16.0 --hash=sha256:%064d\n' 0 >bad-requirements.txt
if hashed/bin/pip install -q --force-reinstall --require-hashes --index-url "$(authed "$simple")" -r bad-requirements.txt >/dev/null 2>&1; then
  echo "a wrong hash was accepted" >&2
  exit 1
fi

step "--index-url for the local, --extra-index-url for the remote"
python3 -m venv split
split/bin/pip install -q --index-url "$(authed "$api/$LOCAL/simple")" --extra-index-url "$(authed "$api/$REMOTE/simple")" relic-native==1.0.0
split/bin/python -c 'import relic_native, six'

step "uv: pip interface, then uv lock and uv sync --frozen"
uv venv -q uvenv
VIRTUAL_ENV="$WORK/uvenv" uv pip install -q --index-url "$(authed "$simple")" relic-native==1.0.0
uvenv/bin/python -c 'import relic_native'
mkdir uvapp && cd uvapp
cat >pyproject.toml <<TOML
[project]
name = "uvapp"
version = "0.1.0"
requires-python = ">=3.9"
dependencies = ["relic-native==1.0.0", "six==1.16.0"]

[[tool.uv.index]]
name = "relic"
url = "$simple"
default = true
TOML
export UV_INDEX_RELIC_USERNAME="$SCENARIO_USER" UV_INDEX_RELIC_PASSWORD="$SCENARIO_PASSWORD"
uv lock
grep -q 'name = "relic-native"' uv.lock
grep -q "$api/$VIRTUAL" uv.lock
rm -rf .venv "$UV_CACHE_DIR"
uv sync --frozen
.venv/bin/python -c 'import relic_native, six'
cd ..

step "poetry: source with credentials, poetry.lock, install from the lock, publish"
mkdir papp && cd papp
cat >pyproject.toml <<TOML
[project]
name = "papp"
version = "0.1.0"
requires-python = ">=3.9"
dependencies = ["relic-native (==1.0.0)"]

[tool.poetry]
package-mode = false
TOML
poetry source add --priority=primary relic "$simple"
poetry config http-basic.relic "$SCENARIO_USER" "$SCENARIO_PASSWORD"
poetry lock
grep -q 'name = "relic-native"' poetry.lock
poetry install --no-root
rm -rf .venv
poetry sync --no-root
.venv/bin/python -c 'import relic_native, six'
cd ../lib
sed -i 's/^version = "1.0.0"/version = "1.1.0"/' pyproject.toml
rm -rf dist && poetry build -q
poetry config repositories.relic-local "$api/$LOCAL"
poetry config http-basic.relic-local "$SCENARIO_USER" "$SCENARIO_PASSWORD"
poetry publish -r relic-local
cd ..
jf rt s "$LOCAL/relic-native/1.1.0/*" | expect_count 2
