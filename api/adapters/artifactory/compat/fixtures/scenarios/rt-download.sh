#!/usr/bin/env bash
# covers: rt download, rt set-props
source "$(dirname "$0")/lib.sh"
delete_repo "$REPO"
create_generic_repo "$REPO"
make_tree
jf rt u "src/(*)" "$REPO/{1}" --flat=false
(cd src && zip -qr ../bundle.zip .)
jf rt u bundle.zip "$REPO/zips/"

step "download explicit file"
jf rt dl "$REPO/README.md" out/explicit/ --flat
cmp out/explicit/README.md src/README.md
step "download wildcard, hierarchy kept"
jf rt dl "$REPO/a/*" out/tree/
test -f out/tree/a/one.txt && test -f out/tree/a/b/two.txt
step "download wildcard, flat"
jf rt dl "$REPO/a/*" out/flat/ --flat
test -f out/flat/one.txt && test -f out/flat/two.txt
step "download and explode"
jf rt dl "$REPO/zips/bundle.zip" out/explode/ --flat --explode
cmp out/explode/README.md src/README.md && cmp out/explode/a/b/two.txt src/a/b/two.txt
step "download newest with sort and limit"
jf rt dl "$REPO/*" out/sorted/ --sort-by created --sort-order desc --limit 1
test -f out/sorted/zips/bundle.zip && [[ "$(find out/sorted -type f | wc -l)" == 1 ]]
step "download by property"
jf rt sp "$REPO/c/*" "picked=yes"
jf rt dl "$REPO/*" out/props/ --props "picked=yes" --flat
test -f out/props/three.bin
step "download with split-count (range requests)"
jf rt dl "$REPO/c/three.bin" out/split/ --flat --split-count 2 --min-split 0
cmp out/split/three.bin src/c/three.bin

delete_repo "$REPO"
