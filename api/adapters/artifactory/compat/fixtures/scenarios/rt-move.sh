#!/usr/bin/env bash
# covers: rt move
source "$(dirname "$0")/lib.sh"
for r in "$REPO" "$REPO2"; do delete_repo "$r"; create_generic_repo "$r"; done
make_tree
jf rt u "src/(*)" "$REPO/{1}" --flat=false

step "move dry run"
jf rt move "$REPO/a/*" "$REPO2/dry/" --dry-run
jf rt s "$REPO/a/*" | expect_count 2
step "move wildcard, hierarchy kept"
jf rt move "$REPO/a/(*)" "$REPO2/tree/{1}" --flat=false
step "move flat"
jf rt move "$REPO/c/*" "$REPO2/flat/" --flat
jf rt s "$REPO2/*" | expect_count 3
jf rt s "$REPO/a/*" | expect_count 0
jf rt s "$REPO/c/*" | expect_count 0

for r in "$REPO" "$REPO2"; do delete_repo "$r"; done
