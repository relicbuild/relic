#!/usr/bin/env bash
# covers:
# tier: platform (chunked upload sessions on S3)
# The OCI distribution-spec conformance suite (v1.1.1) against a local
# Docker repository: pull, push, content discovery (tags list, referrers)
# and content management (deleting tags, manifests and blobs). Needs a
# docker daemon (DOCKER_HOST) that can reach the registry.
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER or a docker daemon missing" >&2
  exit 0
fi
LOCAL=relic-scenario-oci${SCENARIO_SUFFIX:-}
delete_repo "$LOCAL"
printf '{"key":"%s","rclass":"local","packageType":"docker"}\n' "$LOCAL" >repo.json
jf rt repo-create repo.json

step "the conformance suite"
mkdir -p report
set +e
docker run --rm --network host \
  -e OCI_ROOT_URL="$SCENARIO_URL" \
  -e OCI_NAMESPACE="$LOCAL/conformance" \
  -e OCI_CROSSMOUNT_NAMESPACE="$LOCAL/crossmount" \
  -e OCI_USERNAME="$SCENARIO_USER" -e OCI_PASSWORD="$SCENARIO_PASSWORD" \
  -e OCI_TEST_PULL=1 -e OCI_TEST_PUSH=1 -e OCI_TEST_CONTENT_DISCOVERY=1 -e OCI_TEST_CONTENT_MANAGEMENT=1 \
  -e OCI_HIDE_SKIPPED_WORKFLOWS=1 -e OCI_REPORT_DIR=/report \
  -v "$PWD/report:/report" \
  ghcr.io/opencontainers/distribution-spec/conformance:v1.1.1 2>&1 | tee conformance.log
status=${PIPESTATUS[0]}
set -e
grep -E "^(Ran|FAIL|SUCCESS)| Passed .* Failed" conformance.log | tail -5 >&2 || true
[[ $status == 0 ]]
