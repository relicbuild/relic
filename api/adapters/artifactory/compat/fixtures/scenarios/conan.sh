#!/usr/bin/env bash
# covers: conan
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" ]] || ! command -v conan >/dev/null; then
  echo "skipped: SCENARIO_URL or conan missing" >&2
  exit 0
fi
LOCAL=relic-scenario-conan-local
REMOTE=relic-scenario-conan-remote
VIRTUAL=relic-scenario-conan
for r in "$VIRTUAL" "$LOCAL" "$REMOTE"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"conan"},
 {"key":"$REMOTE","rclass":"remote","packageType":"conan","url":"https://center2.conan.io"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"conan","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
B=relic-scenario-conan-build
jf rt bdi "$B" --max-builds 0 >/dev/null 2>&1 || true

export CONAN_HOME="$WORK/conan-home"
conan profile detect >/dev/null 2>&1 || true
conan remote remove conancenter >/dev/null 2>&1 || true
conan remote add relic "$SCENARIO_URL/artifactory/api/conan/$VIRTUAL"

step "create and upload a header-only package"
mkdir -p pkg && cd pkg
mkdir -p include
printf '#pragma once\ninline int relic() { return 42; }\n' >include/relic.h
cat >conanfile.py <<'PY'
from conan import ConanFile
from conan.tools.files import copy


class RelicPkg(ConanFile):
    name = "relicpkg"
    version = "1.0"
    package_type = "header-library"
    exports_sources = "include/*"
    no_copy_source = True

    def package(self):
        copy(self, "*.h", self.source_folder, self.package_folder)

    def package_id(self):
        self.info.clear()
PY
conan create . >/dev/null
jf conan upload "relicpkg/1.0" -r relic --confirm --build-name "$B" --build-number 1
jf rt s "$LOCAL/_/relicpkg/1.0/_/*/export/conanfile.py" | expect_count 1
jf rt bp "$B" 1
jf rt curl -s "/api/build/$B/1" | jq -e '[.buildInfo.modules[].artifacts[]?.name] | index("conanfile.py") != null'

step "list, then install from Relic after clearing the cache"
conan list "relicpkg/*" -r relic | tee list.txt
grep -q "relicpkg/1.0" list.txt
conan remove "relicpkg/*" -c >/dev/null
cd "$WORK"
jf conan install --requires=relicpkg/1.0 -r relic
conan list "relicpkg/1.0:*" | grep -q "relicpkg/1.0"

step "a ConanCenter package through the remote member"
jf conan install --requires=nlohmann_json/3.11.3 -r relic
jf rt s "$REMOTE/_/nlohmann_json/3.11.3/_/*/export/conanfile.py" | jq -e 'length >= 1'

step "remove from the server"
conan remove "relicpkg/1.0" -r relic -c
if conan list "relicpkg/*" -r relic 2>/dev/null | grep -q "relicpkg/1.0"; then
  echo "relicpkg still listed after remove" >&2; exit 1
fi

jf rt bdi "$B" --max-builds 0 >/dev/null 2>&1 || true
for r in "$VIRTUAL" "$LOCAL" "$REMOTE"; do delete_repo "$r"; done
