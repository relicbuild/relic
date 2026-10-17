#!/usr/bin/env bash
# covers: rt copy
source "$(dirname "$0")/lib.sh"
for r in "$REPO" "$REPO2"; do delete_repo "$r"; create_generic_repo "$r"; done
make_tree
jf rt u "src/(*)" "$REPO/{1}" --flat=false

step "copy dry run"
jf rt copy "$REPO/a/*" "$REPO2/dry/" --dry-run
step "copy wildcard, hierarchy kept"
jf rt copy "$REPO/a/(*)" "$REPO2/tree/{1}" --flat=false
step "copy flat"
jf rt copy "$REPO/c/*" "$REPO2/flat/" --flat
jf rt s "$REPO2/*" | expect_count 3
jf rt s "$REPO/*" | expect_count 4

for r in "$REPO" "$REPO2"; do delete_repo "$r"; done
