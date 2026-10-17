#!/usr/bin/env bash
# covers: hugging-face, hugging-face upload, hugging-face download
# tier: platform (a file above the LFS threshold streams to S3)
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" ]] || ! python3 -c 'import huggingface_hub' 2>/dev/null; then
  echo "skipped: SCENARIO_URL or huggingface_hub missing" >&2
  exit 0
fi
LOCAL=relic-scenario-hf-local
VIRTUAL=relic-scenario-hf
for r in "$VIRTUAL" "$LOCAL"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"huggingfaceml"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"huggingfaceml","repositories":"$LOCAL","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
B=relic-scenario-hf-build
jf rt bdi "$B" --max-builds 0 >/dev/null 2>&1 || true

token=$(curl -fsS -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -d expires_in=3600 "$SCENARIO_URL/artifactory/api/security/token" | jq -r .access_token)
jf config add relic-hf --url "$SCENARIO_URL" --access-token "$token" --interactive=false --overwrite
export HF_HOME="$WORK/hf-home" HF_HUB_DISABLE_TELEMETRY=1

step "jf hf upload (regular files and one LFS file) with build info"
mkdir -p model/sub
printf '{"model_type": "relic"}\n' >model/config.json
printf 'vocab\n' >model/sub/vocab.txt
head -c $((11 * 1024 * 1024)) /dev/urandom >model/weights.bin
jf hf upload model relic/demo --repo-key "$VIRTUAL" --server-id relic-hf --build-name "$B" --build-number 1
jf rt s "$LOCAL/models/relic/demo/*" | jq -e 'length == 3'
jf rt bp "$B" 1
jf rt curl -s "/api/build/$B/1" | jq -e '[.buildInfo.modules[].artifacts[]?.name] | (index("weights.bin") != null and index("config.json") != null and index("vocab.txt") != null)'

step "jf hf download"
jf hf download relic/demo --repo-key "$VIRTUAL" --server-id relic-hf | tee download.log
snap=$(find "$HF_HOME" -path '*snapshots*' -name config.json -printf '%h\n' | head -1)
test -n "$snap"
cmp "$snap/config.json" model/config.json
cmp "$snap/sub/vocab.txt" model/sub/vocab.txt
cmp "$snap/weights.bin" model/weights.bin

jf rt bdi "$B" --max-builds 0 >/dev/null 2>&1 || true
for r in "$VIRTUAL" "$LOCAL"; do delete_repo "$r"; done
