#!/usr/bin/env bash
# covers: rt curl, rt direct-download
source "$(dirname "$0")/lib.sh"
delete_repo "$REPO"
create_generic_repo "$REPO"
make_tree
jf rt u "src/(*)" "$REPO/tree/{1}" >/dev/null

step "jf rt curl"
jf rt curl -s "/api/storage/$REPO/tree/a/one.txt" | jq -e '.checksums.sha256 | length == 64' >/dev/null
jf rt curl -s -XPUT "/$REPO/curl.txt" -T src/README.md >/dev/null
test "$(jf rt curl -s "/$REPO/curl.txt")" = "readme"

step "jf rt direct-download"
jf rt ddl "$REPO/tree/a/" out/ --flat=false
test "$(cat out/tree/a/one.txt)" = "one"
test "$(cat out/tree/a/b/two.txt)" = "two"
jf rt ddl "$REPO/tree/c/three.bin" flat/ --flat
test -f flat/three.bin

delete_repo "$REPO"
