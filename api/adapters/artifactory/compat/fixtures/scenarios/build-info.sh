#!/usr/bin/env bash
# covers: rt build-collect-env, rt build-add-git, rt build-add-dependencies, rt build-publish, rt build-append, rt build-promote, rt build-discard, rt build-clean, rt download
source "$(dirname "$0")/lib.sh"
for r in "$REPO" "$REPO2"; do delete_repo "$r"; create_generic_repo "$r"; done
jf rt bdi "$BUILD" --max-builds 0 --delete-artifacts=false >/dev/null 2>&1 || true
make_tree
git init -q . && git add -A && git -c user.name=relic -c user.email=relic@example.com commit -qm fixture

step "collect env, git and dependencies"
jf rt bce "$BUILD" 1
jf rt bag "$BUILD" 1
jf rt bad "$BUILD" 1 "src/c/*"
step "upload with build info"
jf rt u "src/a/*" "$REPO/builds/1/" --build-name "$BUILD" --build-number 1
step "publish"
jf rt bp "$BUILD" 1
step "publish second build and append the first"
jf rt u src/README.md "$REPO/builds/2/" --build-name "$BUILD" --build-number 2
jf rt ba "$BUILD" 2 "$BUILD" 1
jf rt bp "$BUILD" 2
step "promote with copy and status"
jf rt bpr "$BUILD" 1 "$REPO2" --status released --copy --props "promoted=true"
jf rt s "$REPO2/*" | expect_count 2
step "download by build"
jf rt dl "$REPO/*" out/ --build "$BUILD/1" --flat
test -f out/one.txt && test -f out/two.txt
step "discard all but the newest build"
jf rt bdi "$BUILD" --max-builds 1
jf rt curl -s "/api/build/$BUILD" | jq -e '[.buildsNumbers[].uri] == ["/2"]' >/dev/null
step "clean local build info"
jf rt bc "$BUILD" 1

jf rt bdi "$BUILD" --max-builds 0 >/dev/null 2>&1 || true
for r in "$REPO" "$REPO2"; do delete_repo "$r"; done
