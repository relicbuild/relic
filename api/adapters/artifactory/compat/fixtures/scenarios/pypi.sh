#!/usr/bin/env bash
# covers: pip, pip-config, twine
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v twine >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER or twine missing" >&2
  exit 0
fi
LOCAL=relic-scenario-pypi-local
REMOTE=relic-scenario-pypi-remote
VIRTUAL=relic-scenario-pypi
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"pypi"},
 {"key":"$REMOTE","rclass":"remote","packageType":"pypi","url":"https://files.pythonhosted.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"pypi","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
export REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt PIP_CERT=/etc/ssl/certs/ca-certificates.crt PIP_DISABLE_PIP_VERSION_CHECK=1
api="$SCENARIO_TLS_URL/artifactory/api/pypi/$VIRTUAL"
index="${api/:\/\//://$SCENARIO_USER:$SCENARIO_PASSWORD@}/simple"

step "twine uploads a wheel through the virtual"
python3 - <<'PY'
import base64, hashlib, zipfile
files = {
    "relic_demo/__init__.py": b"def hello():\n    return 'relic'\n",
    "relic_demo-1.0.0.dist-info/METADATA": b"Metadata-Version: 2.1\nName: relic-demo\nVersion: 1.0.0\nSummary: Relic e2e\nRequires-Python: >=3.8\n",
    "relic_demo-1.0.0.dist-info/WHEEL": b"Wheel-Version: 1.0\nGenerator: relic-e2e\nRoot-Is-Purelib: true\nTag: py3-none-any\n",
}
record = []
for name, data in files.items():
    digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
    record.append(f"{name},sha256={digest},{len(data)}")
record.append("relic_demo-1.0.0.dist-info/RECORD,,")
files["relic_demo-1.0.0.dist-info/RECORD"] = ("\n".join(record) + "\n").encode()
with zipfile.ZipFile("relic_demo-1.0.0-py3-none-any.whl", "w") as z:
    for name, data in files.items():
        z.writestr(name, data)
PY
twine upload --non-interactive --repository-url "$api" -u "$SCENARIO_USER" -p "$SCENARIO_PASSWORD" relic_demo-1.0.0-py3-none-any.whl
jf rt s "$LOCAL/relic-demo/1.0.0/relic_demo-1.0.0-py3-none-any.whl" | expect_count 1

step "jf twine upload with build info"
python3 - <<'PY'
import zipfile
src = zipfile.ZipFile("relic_demo-1.0.0-py3-none-any.whl")
with zipfile.ZipFile("relic_demo-1.1.0-py3-none-any.whl", "w") as z:
    for i in src.infolist():
        z.writestr(i.filename.replace("1.0.0", "1.1.0"), src.read(i).replace(b"1.0.0", b"1.1.0"))
PY
jf pipc --server-id-resolve relic-tls --repo-resolve "$VIRTUAL" --server-id-deploy relic-tls --repo-deploy "$LOCAL"
jf twine upload relic_demo-1.1.0-py3-none-any.whl --build-name "$BUILD-twine" --build-number 1
jf rt bp "$BUILD-twine" 1
jf rt s --build "$BUILD-twine/1" "$LOCAL/" | expect_count 1

step "pip installs it and a pypi.org project through the virtual"
python3 -m venv venv
venv/bin/pip install -q --index-url "$index" relic-demo==1.0.0 six==1.16.0
venv/bin/python -c 'import relic_demo, six; assert relic_demo.hello() == "relic"'
jf rt s "$REMOTE/packages/*/six-1.16.0-py2.py3-none-any.whl" | expect_count 1

step "jf pip install with build info"
python3 -m venv venv2
(
  source venv2/bin/activate
  jf pipc --server-id-resolve relic-tls --repo-resolve "$VIRTUAL"
  jf pip install relic-demo==1.0.0 six==1.16.0 --build-name "$BUILD-pip" --build-number 1
)
jf rt bp "$BUILD-pip" 1
jf rt curl -s "/api/build/$BUILD-pip/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 2' >/dev/null

for r in "${ALL[@]}"; do delete_repo "$r"; done
