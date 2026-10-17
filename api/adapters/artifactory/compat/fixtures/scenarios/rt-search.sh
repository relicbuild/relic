#!/usr/bin/env bash
# covers: rt search
source "$(dirname "$0")/lib.sh"
delete_repo "$REPO"
create_generic_repo "$REPO"
make_tree
jf rt u "src/(*)" "$REPO/{1}" --flat=false --target-props "kind=fixture"

step "search wildcard"
jf rt s "$REPO/*" | expect_count 4
step "search non-recursive"
jf rt s "$REPO/*" --recursive=false | expect_count 1
step "search by props"
jf rt s "$REPO/*" --props "kind=fixture" | expect_count 4
step "search count"
[[ "$(jf rt s "$REPO/*" --count)" == "4" ]]
step "search including dirs"
jf rt s "$REPO/*" --include-dirs | expect_count 7
step "search with sort and limit"
jf rt s "$REPO/*" --sort-by name --sort-order asc --limit 2 --offset 1 | expect_count 2
step "search with exclusions"
jf rt s "$REPO/*" --exclusions "*.bin" | expect_count 3

delete_repo "$REPO"
