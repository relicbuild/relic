#!/usr/bin/env bash
# covers: poetry, poetry-config, pipenv, pipenv-config
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" ]] || ! command -v poetry >/dev/null || ! command -v pipenv >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL, poetry or pipenv missing" >&2
  exit 0
fi
REMOTE=relic-scenario-pytools-remote
delete_repo "$REMOTE"
printf '[{"key":"%s","rclass":"remote","packageType":"pypi","url":"https://files.pythonhosted.org"}]\n' "$REMOTE" >repos.json
jf rt rc repos.json
export REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt PIP_CERT=/etc/ssl/certs/ca-certificates.crt \
  PIP_DISABLE_PIP_VERSION_CHECK=1 POETRY_VIRTUALENVS_IN_PROJECT=true PIPENV_VENV_IN_PROJECT=1 PIPENV_NOSPIN=1 \
  POETRY_CACHE_DIR="$WORK/poetry-cache" PIPENV_CACHE_DIR="$WORK/pipenv-cache" XDG_CACHE_HOME="$WORK/cache"

step "jf poetry install"
mkdir -p "$WORK/poetry-app" && cd "$WORK/poetry-app"
cat >pyproject.toml <<'TOML'
[tool.poetry]
name = "relic-poetry-app"
version = "0.1.0"
description = "relic e2e"
authors = ["Relic <relic@example.com>"]
package-mode = false

[tool.poetry.dependencies]
python = "^3.9"
six = "1.16.0"
TOML
jf poetry-config --server-id-resolve relic-tls --repo-resolve "$REMOTE"
jf poetry install
grep -q 'name = "six"' poetry.lock
grep -q "api/pypi/$REMOTE" pyproject.toml

step "jf pipenv install"
mkdir -p "$WORK/pipenv-app" && cd "$WORK/pipenv-app"
jf pipenv-config --server-id-resolve relic-tls --repo-resolve "$REMOTE"
jf pipenv install six==1.16.0
.venv/bin/python -c 'import six; assert six.__version__ == "1.16.0"'

cd "$WORK"
jf rt s "$REMOTE-cache/*six*" | jq -e 'length >= 1'
delete_repo "$REMOTE"
