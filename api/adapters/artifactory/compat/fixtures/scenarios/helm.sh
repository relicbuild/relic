#!/usr/bin/env bash
# covers: helm
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_REGISTRY:-}" ]] || ! command -v helm >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_REGISTRY or helm missing" >&2
  exit 0
fi
LOCAL=relic-scenario-helm-local
OCI=relic-scenario-helm-oci
VIRTUAL=relic-scenario-helm
ALL=("$VIRTUAL" "$LOCAL" "$OCI")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"helm"},
 {"key":"$OCI","rclass":"local","packageType":"helm"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"helm","repositories":"$LOCAL"}]
JSON
jf rt rc repos.json
export HELM_CONFIG_HOME="$WORK/helm/config" HELM_CACHE_HOME="$WORK/helm/cache" HELM_DATA_HOME="$WORK/helm/data"

step "package and deploy a classic chart"
helm create -q demo >/dev/null 2>&1 || helm create demo >/dev/null
sed -i 's/^version: .*/version: 0.3.0/' demo/Chart.yaml
helm package demo >/dev/null
jf rt u demo-0.3.0.tgz "$LOCAL/"
jf rt curl -sf "/$LOCAL/demo-0.3.0.tgz" -o /dev/null

step "helm repo add and pull through the virtual"
helm repo add relic "$SCENARIO_TLS_URL/artifactory/api/helm/$VIRTUAL" --username "$SCENARIO_USER" --password "$SCENARIO_PASSWORD"
helm search repo relic/demo --version 0.3.0 | grep -q demo
rm -f demo-0.3.0.tgz
helm pull relic/demo --version 0.3.0
test -f demo-0.3.0.tgz

step "jf helm push over OCI with build info"
echo "$SCENARIO_PASSWORD" | helm registry login "$SCENARIO_REGISTRY" -u "$SCENARIO_USER" --password-stdin "${REGISTRY_INSECURE[@]}"
sed -i 's/^version: .*/version: 0.4.0/' demo/Chart.yaml
jf helm package demo
jf helm push demo-0.4.0.tgz "oci://$SCENARIO_REGISTRY/$OCI" "${REGISTRY_PLAIN_HTTP[@]}" --build-name "$BUILD-helm" --build-number 1
jf rt bp "$BUILD-helm" 1
jf rt s "$OCI/demo/0.4.0/manifest.json" | expect_count 1
jf rt s --build "$BUILD-helm/1" "$OCI/" | expect_count 3
mkdir pulled
helm pull "oci://$SCENARIO_REGISTRY/$OCI/demo" --version 0.4.0 "${REGISTRY_PLAIN_HTTP[@]}" -d pulled
test -f pulled/demo-0.4.0.tgz

for r in "${ALL[@]}"; do delete_repo "$r"; done
