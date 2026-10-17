#!/usr/bin/env bash
# covers: rt delete
source "$(dirname "$0")/lib.sh"
delete_repo "$REPO"
create_generic_repo "$REPO"
make_tree
jf rt u "src/(*)" "$REPO/{1}" --flat=false
jf rt sp "$REPO/c/*" "doomed=true"

step "delete dry run"
jf rt del "$REPO/a/*" --dry-run --quiet
jf rt s "$REPO/a/*" | expect_count 2
step "delete explicit"
jf rt del "$REPO/README.md" --quiet
step "restore from the trash can"
jf rt curl -sf -XPOST "/api/trash/restore/$REPO/README.md" >/dev/null
jf rt s "$REPO/README.md" | expect_count 1
jf rt del "$REPO/README.md" --quiet
step "delete by props"
jf rt del "$REPO/*" --props "doomed=true" --quiet
jf rt s "$REPO/*" | expect_count 2
step "delete wildcard"
jf rt del "$REPO/a/*" --quiet
jf rt s "$REPO/*" | expect_count 0

delete_repo "$REPO"
