#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v conan >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER or conan missing" >&2
  exit 0
fi
LOCAL=relic-scenario-nconan-local
REMOTE=relic-scenario-nconan-remote
VIRTUAL=relic-scenario-nconan
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"conan"},
 {"key":"$REMOTE","rclass":"remote","packageType":"conan","url":"https://center2.conan.io"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"conan","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
export CONAN_HOME="$WORK/conan-home" CONAN_NON_INTERACTIVE=1 REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
conan profile detect >/dev/null 2>&1 || true
conan remote remove conancenter >/dev/null 2>&1 || true
conan remote add relic "$SCENARIO_TLS_URL/conan/$VIRTUAL"

recipe() { # recipe <dir> <version> <value>
  mkdir -p "$1/include"
  printf '#pragma once\ninline int relic() { return %s; }\n' "$3" >"$1/include/relic.h"
  cat >"$1/conanfile.py" <<PY
from conan import ConanFile
from conan.tools.files import copy


class RelicNative(ConanFile):
    name = "relicnative"
    version = "$2"
    package_type = "header-library"
    exports_sources = "include/*"
    no_copy_source = True

    def package(self):
        copy(self, "*.h", self.source_folder, self.package_folder)

    def package_id(self):
        self.info.clear()
PY
}

step "anonymous access is refused"
# On 401 conan asks for credentials (EOF without a terminal), reports the
# remote's error in its output and still exits 0.
conan list "*" -r relic --format=json </dev/null >anon.json 2>&1 || true
grep -q "needs authentication" anon.json

step "conan remote login, create and upload two revisions and two versions"
conan remote login relic "$SCENARIO_USER" -p "$SCENARIO_PASSWORD"
recipe r1 1.0 41 && conan create r1 >/dev/null && conan upload "relicnative/1.0" -r relic -c
recipe r1 1.0 42 && conan create r1 >/dev/null && conan upload "relicnative/1.0" -r relic -c
recipe r2 1.1 43 && conan create r2 >/dev/null && conan upload "relicnative/1.1" -r relic -c
jf rt s "$LOCAL/_/relicnative/*/_/*/export/conanfile.py" | expect_count 3

step "conan list: versions, revisions and packages"
conan list "relicnative/*" -r relic --format=json >versions.json
jq -e '.relic | has("relicnative/1.0") and has("relicnative/1.1")' versions.json >/dev/null
conan list "relicnative/1.0#*" -r relic --format=json >revisions.json
jq -e '.relic["relicnative/1.0"].revisions | length == 2' revisions.json >/dev/null
conan list "relicnative/1.1:*" -r relic --format=json | jq -e '[.. | .packages? // empty] | add | length == 1' >/dev/null

step "a lockfile, then a locked install into an empty cache"
# In its own directory: conan picks up a conan.lock in the working directory.
mkdir locked && cd locked
conan lock create --requires="relicnative/[>=1.0 <1.1]" -r relic --lockfile-out=conan.lock
grep -q "relicnative/1.0#" conan.lock
conan remove "*" -c >/dev/null
conan install --requires="relicnative/[>=1.0 <1.1]" --lockfile=conan.lock -r relic
conan list "relicnative/1.0#*" | grep -q "relicnative/1.0"
cd ..

step "a ConanCenter package through the remote member"
conan install --requires=nlohmann_json/3.11.3 -r relic
jf rt s "$REMOTE/_/nlohmann_json/3.11.3/_/*/export/conanfile.py" | jq -e 'length >= 1' >/dev/null

step "conan remove a revision, then the recipe, on the server"
latest=$(jq -r '.relic["relicnative/1.0"].revisions | to_entries | max_by(.value.timestamp) | .key' revisions.json)
conan remove "relicnative/1.0#$latest" -r relic -c
conan list "relicnative/1.0#*" -r relic --format=json | jq -e '.relic["relicnative/1.0"].revisions | length == 1' >/dev/null
conan remove "relicnative/1.1" -r relic -c
conan list "relicnative/*" -r relic --format=json >after.json
cat after.json
jq -e '.relic | has("relicnative/1.0") and (has("relicnative/1.1") | not)' after.json >/dev/null
