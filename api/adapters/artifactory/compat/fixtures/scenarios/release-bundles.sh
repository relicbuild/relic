#!/usr/bin/env bash
# covers: release-bundle-create, release-bundle-update, release-bundle-finalize, release-bundle-promote, release-bundle-search, release-bundle-annotate, release-bundle-export, release-bundle-import, release-bundle-delete-local, release-bundle-delete-remote, rt download --bundle
source "$(dirname "$0")/lib.sh"
RB=relic-scenario-app
for v in 1.0 2.0 3.0; do jf rbdell "$RB" "$v" --quiet >/dev/null 2>&1 || true; done
for r in "$REPO" "$REPO2"; do delete_repo "$r"; done
create_generic_repo "$REPO"
# shellcheck disable=SC2016
printf '{"key":"${key}","rclass":"local","packageType":"generic","environments":"PROD"}\n' >repo-prod.json
jf rt repo-create repo-prod.json --vars "key=$REPO2"
jf rt bdi "$BUILD" --max-builds 0 --delete-artifacts=false >/dev/null 2>&1 || true
make_tree

step "publish a build"
jf rt u "src/a/*" "$REPO/app/" --build-name "$BUILD" --build-number 1
jf rt bp "$BUILD" 1
step "create from the build"
jf rbc "$RB" 1.0 --build-name "$BUILD" --build-number 1
step "create from a spec"
printf '{"files":[{"pattern":"%s/app/*"}]}\n' "$REPO" >spec.json
jf rbc "$RB" 2.0 --spec spec.json
step "search"
names=$(jf rbs names)
echo "$names"
grep -q "$RB" <<<"$names"
versions=$(jf rbs versions "$RB")
echo "$versions"
grep -q "2.0" <<<"$versions"
step "promote to PROD"
jf rbp "$RB" 1.0 PROD
jf rt s "$REPO2/app/*" | expect_count 2
step "download by bundle"
jf rt dl "*" out/ --bundle "$RB/1.0" --flat
test -f out/one.txt && test -f out/two.txt
step "annotate"
jf rba "$RB" 1.0 --tag rc --properties "team=core"
jf rt s "release-bundles-v2/$RB/1.0/release-bundle.json.evd" --props team=core | expect_count 1
step "export"
jf rbe "$RB" 1.0 exported/
archive=$(find exported -name '*.zip' | head -1)
test -s "$archive"
step "delete locally, then import the archive"
jf rbdell "$RB" 1.0 --quiet
jf rbi "$archive"
jf rt dl "*" again/ --bundle "$RB/1.0" --flat
test -f again/one.txt && test -f again/two.txt
step "draft, update, finalize"
printf '{"files":[{"pattern":"%s/app/b/*"}]}\n' "$REPO" >spec-b.json
jf rbc "$RB" 3.0 --spec spec-b.json --draft
jf rbu "$RB" 3.0 --add --spec spec.json
jf rbf "$RB" 3.0
jf rt dl "*" draft-out/ --bundle "$RB/3.0" --flat
test -f draft-out/one.txt && test -f draft-out/two.txt
step "delete promotions and remote copies"
jf rbdell "$RB" 1.0 PROD --quiet
curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" "$SCENARIO_URL/lifecycle/api/v2/promotion/records/$RB/1.0" |
  jq -e '.total == 0' >/dev/null
jf rbdelr "$RB" 2.0 --quiet
jf rbs versions "$RB" | grep -q "2.0"

for v in 1.0 2.0 3.0; do jf rbdell "$RB" "$v" --quiet >/dev/null 2>&1 || true; done
jf rt bdi "$BUILD" --max-builds 0 >/dev/null 2>&1 || true
for r in "$REPO" "$REPO2"; do delete_repo "$r"; done
