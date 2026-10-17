#!/usr/bin/env bash
# covers: rt upload, rt search, rt repo-create, rt repo-delete
source "$(dirname "$0")/lib.sh"
delete_repo "$REPO"
create_generic_repo "$REPO"
make_tree

step "upload single file to explicit path"
jf rt u src/README.md "$REPO/explicit/README.md"

step "upload wildcard, flat"
jf rt u "src/a/*" "$REPO/flat/" --flat
step "upload wildcard, hierarchy kept"
jf rt u "src/(*)" "$REPO/tree/{1}" --flat=false
step "upload non-recursive"
jf rt u "src/*" "$REPO/nonrec/" --recursive=false
step "upload regexp"
jf rt u "src/a/(.*)\.txt" "$REPO/regexp/{1}.txt" --regexp
step "upload with target props"
jf rt u "src/c/*" "$REPO/props/" --target-props "stage=dev;team=a,b"
step "upload as zip archive"
jf rt u "src/a/*" "$REPO/archive/a.zip" --archive zip
step "upload archive and explode"
(cd src && zip -qr ../bundle.zip .)
jf rt u bundle.zip "$REPO/explode/" --explode
step "re-upload identical file (checksum deploy)"
jf rt u src/README.md "$REPO/explicit/README.md"
step "upload with sync-deletes"
jf rt u src/README.md "$REPO/tree/" --sync-deletes "$REPO/tree/"
step "upload with a file spec"
cat >spec.json <<SPEC
{"files":[{"pattern":"src/c/*","target":"$REPO/spec/","props":"from=spec"}]}
SPEC
jf rt u --spec spec.json

step "verify"
jf rt s "$REPO/explicit/*" | expect_count 1
jf rt s "$REPO/flat/*" | expect_count 2
jf rt s "$REPO/props/*" --props "stage=dev" | expect_count 1
jf rt s "$REPO/nonrec/*" | expect_count 1
jf rt s "$REPO/regexp/*" | expect_count 2
jf rt s "$REPO/archive/a.zip" | expect_count 1
jf rt s "$REPO/explode/*" | expect_count 4
jf rt s "$REPO/tree/*" | expect_count 1
jf rt s "$REPO/spec/*" --props "from=spec" | expect_count 1

delete_repo "$REPO"
