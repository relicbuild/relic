#!/usr/bin/env bash
# covers: rt set-props, rt delete-props
source "$(dirname "$0")/lib.sh"
delete_repo "$REPO"
create_generic_repo "$REPO"
make_tree
jf rt u "src/(*)" "$REPO/{1}" --flat=false

step "set-props wildcard, multi-valued"
jf rt sp "$REPO/a/*" "env=dev;owners=x,y"
jf rt s "$REPO/*" --props "env=dev" | expect_count 2
step "set-props including dirs"
jf rt sp "$REPO/c" "dir=yes" --include-dirs
jf rt curl -s "/api/storage/$REPO/c?properties=dir" | jq -e '.properties.dir == ["yes"]' >/dev/null
step "delete-props"
jf rt delp "$REPO/a/*" "env,owners"
jf rt s "$REPO/*" --props "env=dev" | expect_count 0
jf rt s "$REPO/*" --props "owners=x" | expect_count 0

delete_repo "$REPO"
