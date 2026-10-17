#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v helm >/dev/null || ! command -v helmfile >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER, helm or helmfile missing" >&2
  exit 0
fi
LOCAL=relic-scenario-nhelm-local
REMOTE=relic-scenario-nhelm-jetstack
OCI=relic-scenario-nhelm-oci
VIRTUAL=relic-scenario-nhelm
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE" "$OCI")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"helm"},
 {"key":"$REMOTE","rclass":"remote","packageType":"helm","url":"https://charts.jetstack.io"},
 {"key":"$OCI","rclass":"local","packageType":"helm"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"helm","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
export HELM_CONFIG_HOME="$WORK/helm/config" HELM_CACHE_HOME="$WORK/helm/cache" HELM_DATA_HOME="$WORK/helm/data"
host=${SCENARIO_TLS_URL#*://}
host=${host%%/*}
repo_url="$SCENARIO_TLS_URL/helm/$VIRTUAL"

chart() { # chart <name> <version>: a packaged chart in ./<name>-<version>.tgz
  rm -rf "$1" && helm create "$1" >/dev/null
  sed -i "s/^version: .*/version: $2/" "$1/Chart.yaml"
  helm package "$1" >/dev/null
}

step "upload classic charts with curl"
for v in 1.0.0 1.1.0; do
  chart web "$v"
  curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -T "web-$v.tgz" "$SCENARIO_TLS_URL/helm/$LOCAL/web-$v.tgz" >/dev/null
done

step "helm repo add, update, search, pull, show, template through the virtual"
helm repo add relic "$repo_url" --username "$SCENARIO_USER" --password "$SCENARIO_PASSWORD"
helm repo update relic
helm search repo relic/web --versions | grep -q "1.0.0"
[[ "$(helm search repo relic/web -o json | jq -r '.[0].version')" == "1.1.0" ]]
helm search repo relic/cert-manager --version v1.16.2 -o json | jq -e 'any(.[]; .name == "relic/cert-manager")' >/dev/null
mkdir pulled
helm pull relic/web --version 1.0.0 -d pulled && test -f pulled/web-1.0.0.tgz
helm pull relic/cert-manager --version v1.16.2 -d pulled && test -f pulled/cert-manager-v1.16.2.tgz
helm show chart relic/web | grep -q "^version: 1.1.0"
helm template demo relic/web --version 1.0.0 | grep -q "kind: Deployment"
jf rt s "$REMOTE/charts/cert-manager-v1.16.2.tgz" | expect_count 1

step "helm dependency update resolves dependencies from Relic"
helm create app >/dev/null
cat >>app/Chart.yaml <<YAML
dependencies:
  - name: web
    version: 1.1.0
    repository: $repo_url
  - name: cert-manager
    version: v1.16.2
    repository: "@relic"
YAML
helm dependency update app
test -f app/charts/web-1.1.0.tgz && test -f app/charts/cert-manager-v1.16.2.tgz

step "OCI: helm registry login, push, show, pull, template, logout"
echo "$SCENARIO_PASSWORD" | helm registry login "$host" -u "$SCENARIO_USER" --password-stdin
chart api 2.0.0
helm push api-2.0.0.tgz "oci://$host/$OCI" 2>&1 | tee push.log
grep -q "Digest: sha256:" push.log
helm show chart "oci://$host/$OCI/api" --version 2.0.0 | grep -q "^name: api"
helm pull "oci://$host/$OCI/api" --version 2.0.0 -d pulled && test -f pulled/api-2.0.0.tgz
helm template demo "oci://$host/$OCI/api" --version 2.0.0 | grep -q "kind: Service"
jf rt s "$OCI/api/2.0.0/manifest.json" | expect_count 1
helm registry logout "$host"
mkdir -p "$WORK/anon"
if helm pull "oci://$host/$OCI/api" --version 2.0.0 -d "$WORK/anon" >/dev/null 2>&1; then
  echo "anonymous OCI pull was accepted" >&2
  exit 1
fi

step "helmfile deps, template and fetch from a classic and an OCI repository"
mkdir hf && cd hf
cat >helmfile.yaml <<YAML
repositories:
  - name: relic
    url: $repo_url
    username: $SCENARIO_USER
    password: $SCENARIO_PASSWORD
  - name: relic-oci
    url: $host/$OCI
    oci: true
    username: $SCENARIO_USER
    password: $SCENARIO_PASSWORD
releases:
  - name: web
    namespace: apps
    chart: relic/web
    version: 1.1.0
  - name: api
    namespace: apps
    chart: relic-oci/api
    version: 2.0.0
  - name: cert-manager
    namespace: cert-manager
    chart: relic/cert-manager
    version: v1.16.2
    values:
      - crds:
          enabled: false
YAML
helmfile deps
test -f helmfile.lock
helmfile template >rendered.yaml
grep -q "name: web" rendered.yaml && grep -q "name: api" rendered.yaml && grep -q "cert-manager" rendered.yaml
helmfile fetch --output-dir fetched
find fetched -name 'web-1.1.0.tgz' | grep -q . && find fetched -name 'api-2.0.0.tgz' | grep -q .
cd ..
