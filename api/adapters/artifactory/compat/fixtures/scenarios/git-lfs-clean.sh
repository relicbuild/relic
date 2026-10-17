#!/usr/bin/env bash
# covers: rt git-lfs-clean
source "$(dirname "$0")/lib.sh"
: "${SCENARIO_URL:?SCENARIO_URL must be set}"
delete_repo "$REPO"
create_generic_repo "$REPO"

oid() { printf '%s' "$1" | sha256sum | cut -d' ' -f1; }
KEEP=$(oid "kept object")
DROP=$(oid "dropped object")
for content in "kept object" "dropped object"; do
  o=$(oid "$content")
  printf '%s' "$content" >"$o"
  jf rt upload "$o" "$REPO/objects/${o:0:2}/${o:2:2}/$o" >/dev/null
done

step "a git repository whose LFS pointer names one object"
git init -q -b main repo && cd repo
git config user.email scenario@relic.test && git config user.name scenario
printf '[lfs]\n\turl = %s/artifactory/api/lfs/%s\n' "$SCENARIO_URL" "$REPO" >.lfsconfig
printf 'version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize 11\n' "$KEEP" >big.bin
git add . && git commit -q -m pointer

step "--dry-run deletes nothing"
jf rt git-lfs-clean --refs 'refs/heads/*' --dry-run --quiet
jf rt search "$REPO/objects/" | expect_count 2

step "git-lfs-clean deletes the unreferenced object"
jf rt git-lfs-clean --refs 'refs/heads/*' --quiet
jf rt search "$REPO/objects/" | jq -e --arg k "$KEEP" 'length == 1 and (.[0].path | endswith($k))' >/dev/null
if jf rt search "$REPO/objects/" | jq -e --arg d "$DROP" 'any(.path | endswith($d))' >/dev/null; then
  echo "the unreferenced object was not deleted" >&2
  exit 1
fi

cd .. && delete_repo "$REPO"
