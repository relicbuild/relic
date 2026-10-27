#!/usr/bin/env bash
# covers:
# git-lfs with a Git LFS repository as its lfs.url: git push uploads the
# objects through the batch API, a clone downloads them, and git lfs pull
# fetches them again at the path jf's clients use (/artifactory/api/lfs).
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! git lfs version >/dev/null 2>&1; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER or git-lfs missing" >&2
  exit 0
fi
LOCAL=relic-scenario-nlfs-local
delete_repo "$LOCAL"
printf '[{"key":"%s","rclass":"local","packageType":"gitlfs"}]\n' "$LOCAL" >repos.json
jf rt rc repos.json
trap 'delete_repo "$LOCAL"; rm -rf "$WORK"' EXIT
export JFROG_CLI_HOME_DIR="${JFROG_CLI_HOME_DIR:-$HOME/.jfrog}"
export HOME="$WORK/home"
mkdir -p "$HOME"
host=${SCENARIO_TLS_URL#https://}
host=${host%%/*}
user=$(printf '%s' "$SCENARIO_USER" | jq -sRr @uri)
password=$(printf '%s' "$SCENARIO_PASSWORD" | jq -sRr @uri)
printf 'https://%s:%s@%s\n' "$user" "$password" "$host" >"$HOME/.git-credentials"
chmod 600 "$HOME/.git-credentials"
git config --global credential.helper store
git config --global user.email scenario@relic.test
git config --global user.name scenario
git config --global init.defaultBranch main
git lfs install >/dev/null

step "git push uploads the LFS objects to the repository"
git init -q --bare origin.git
git init -q work && cd work
printf '[lfs]\n\turl = %s/lfs/%s\n' "$SCENARIO_TLS_URL" "$LOCAL" >.lfsconfig
git lfs track '*.bin' >/dev/null
head -c $((2 * 1024 * 1024)) /dev/urandom >model.bin
printf 'small object\n' >notes.bin
git add .gitattributes .lfsconfig model.bin notes.bin
git commit -q -m objects
git remote add origin ../origin.git
git push -q origin main
git lfs ls-files | grep -q model.bin
cd ..
for f in model.bin notes.bin; do
  oid=$(sha256sum "work/$f" | cut -d' ' -f1)
  jf rt s "$LOCAL/objects/${oid:0:2}/${oid:2:2}/$oid" | expect_count 1
done

step "a second push of the same objects uploads nothing new"
(cd work && git lfs push --all origin main)
jf rt s "$LOCAL/objects/" | expect_count 2

step "git clone downloads the objects"
git clone -q origin.git clone
cmp clone/model.bin work/model.bin
cmp clone/notes.bin work/notes.bin

step "git lfs pull at the path jf's clients use"
GIT_LFS_SKIP_SMUDGE=1 git clone -q origin.git jfpath
cd jfpath
git config lfs.url "$SCENARIO_TLS_URL/artifactory/api/lfs/$LOCAL"
grep -q 'oid sha256:' model.bin
git lfs pull
cmp model.bin ../work/model.bin
cd ..
