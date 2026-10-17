#!/usr/bin/env bash
# covers: evd, evd generate-key-pair, evd create-evidence, evd get-evidence, evd verify-evidence
source "$(dirname "$0")/lib.sh"
RB=relic-scenario-evd
jf rbdell "$RB" 1.0 --quiet >/dev/null 2>&1 || true
delete_repo "$REPO"
create_generic_repo "$REPO"
jf rt bdi "$BUILD" --max-builds 0 --delete-artifacts=false >/dev/null 2>&1 || true
make_tree

step "evidence needs an access token, not basic auth"
token=$(curl -fsS -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -d expires_in=3600 "$SCENARIO_URL/artifactory/api/security/token" | jq -r .access_token)
jf config add relic-evd --url "$SCENARIO_URL" --access-token "$token" --interactive=false --overwrite
evd() { jf evd "$@" --server-id relic-evd; }

step "generate a key pair and upload the public key"
alias="relic-e2e-$RANDOM$RANDOM"
evd generate-key-pair --key-alias "$alias"
test -f evidence.key && test -f evidence.pub
printf '{"builder":"relic-e2e","result":"pass"}\n' >predicate.json

step "artifact evidence"
jf rt u src/a/one.txt "$REPO/app/" --flat --build-name "$BUILD" --build-number 1
evd create --subject-repo-path "$REPO/app/one.txt" --predicate predicate.json \
  --predicate-type https://relic.build/test-result/v1 --key evidence.key --key-alias "$alias" 2>&1 | tee create.log
grep -q "verified" create.log
evd get --subject-repo-path "$REPO/app/one.txt" --format json --include-predicate --output got.json
jq -e '.result.evidence | length == 1' got.json
evd verify --subject-repo-path "$REPO/app/one.txt" --public-keys evidence.pub
evd verify --subject-repo-path "$REPO/app/one.txt" --use-artifactory-keys

step "build evidence"
jf rt bp "$BUILD" 1
evd create --build-name "$BUILD" --build-number 1 --predicate predicate.json \
  --predicate-type https://relic.build/test-result/v1 --key evidence.key --key-alias "$alias"
evd verify --build-name "$BUILD" --build-number 1 --public-keys evidence.pub

step "release bundle evidence"
jf rbc "$RB" 1.0 --build-name "$BUILD" --build-number 1
evd create --release-bundle "$RB" --release-bundle-version 1.0 --predicate predicate.json \
  --predicate-type https://relic.build/test-result/v1 --key evidence.key --key-alias "$alias"
evd get --release-bundle "$RB" --release-bundle-version 1.0 --format json --output rb.json
grep -q "test-result" rb.json
evd verify --release-bundle "$RB" --release-bundle-version 1.0 --use-artifactory-keys

step "a changed artifact no longer verifies"
printf 'changed\n' >one.txt
jf rt u one.txt "$REPO/app/one.txt"
if evd verify --subject-repo-path "$REPO/app/one.txt" --public-keys evidence.pub --format json >changed.json; then
  echo "verify passed for changed content" >&2
  exit 1
fi
jq -e '[.evidenceVerifications[].verificationResult.sha256VerificationStatus] | index("failed") != null' changed.json

jf rbdell "$RB" 1.0 --quiet >/dev/null 2>&1 || true
jf rt bdi "$BUILD" --max-builds 0 >/dev/null 2>&1 || true
delete_repo "$REPO"
