#!/usr/bin/env bash
# covers: rt users-create, rt user-create, rt users-delete, rt group-create, rt group-add-users, rt group-delete, rt permission-target-create, rt permission-target-update, rt permission-target-delete, rt access-token-create, access-token-create
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" ]]; then
  echo "skipped: SCENARIO_URL not set" >&2
  exit 0
fi
USER1=relic-scenario-alice
USER2=relic-scenario-bob
GROUP=relic-scenario-devs
PT=relic-scenario-pt
cleanup() {
  jf rt ptdel "$PT" --quiet >/dev/null 2>&1 || true
  jf rt gdel "$GROUP" --quiet >/dev/null 2>&1 || true
  jf rt udel "$USER1" --quiet >/dev/null 2>&1 || true
  jf rt udel "$USER2" --quiet >/dev/null 2>&1 || true
  delete_repo "$REPO"
  delete_repo "$REPO2"
}
cleanup
for r in "$REPO" "$REPO2"; do create_generic_repo "$r"; done

step "users-create from CSV, then refuse a duplicate without --replace"
printf 'username,password,email\n%s,Alice-pw-1,alice@example.com\n' "$USER1" >users.csv
jf rt uc --csv users.csv
if jf rt uc --csv users.csv 2>err.txt; then echo "duplicate user accepted" >&2; exit 1; fi
grep -q "already exists" err.txt
jf rt uc --csv users.csv --replace

step "user-create makes one user"
jf rt user-create "$USER2" Bob-pw-1 bob@example.com
jf rt curl -sf "/api/security/users/$USER2" | jq -e '.email == "bob@example.com"' >/dev/null

step "group-create and group-add-users"
jf rt gc "$GROUP"
if jf rt gc "$GROUP" 2>/dev/null; then echo "duplicate group accepted" >&2; exit 1; fi
jf rt gau "$GROUP" "$USER1"

step "permission target: devs may read and deploy in the first repo only"
cat >pt.json <<JSON
{"name":"$PT","repo":{"repositories":"$REPO","include-patterns":"**","actions-groups":{"$GROUP":"read,write"}}}
JSON
jf rt ptc pt.json

ALICE=(--url "$SCENARIO_URL/artifactory" --user "$USER1" --password Alice-pw-1)
make_tree
step "alice can deploy and download in the granted repo"
jf rt u src/README.md "$REPO/alice/" --flat "${ALICE[@]}"
jf rt dl "$REPO/alice/README.md" got/ --flat "${ALICE[@]}"
cmp got/README.md src/README.md
step "alice cannot delete, and cannot deploy elsewhere"
if jf rt del "$REPO/alice/README.md" --quiet "${ALICE[@]}" 2>/dev/null; then
  jf rt s "$REPO/alice/README.md" | expect_count 1
fi
jf rt s "$REPO/alice/README.md" | expect_count 1
if jf rt u src/README.md "$REPO2/alice/" --flat --fail-no-op "${ALICE[@]}" 2>/dev/null; then echo "deploy outside the grant succeeded" >&2; exit 1; fi
jf rt s "$REPO2/*" | expect_count 0
step "alice's searches only see readable repos"
jf rt u src/README.md "$REPO2/hidden/" --flat
jf rt s "$REPO2/*" "${ALICE[@]}" | expect_count 0
step "alice cannot administer"
if jf rt gc other-group "${ALICE[@]}" 2>/dev/null; then echo "non-admin created a group" >&2; exit 1; fi

step "permission-target-update adds delete"
sed -i 's/"read,write"/"read,write,delete"/' pt.json
jf rt ptu pt.json
jf rt del "$REPO/alice/README.md" --quiet "${ALICE[@]}"
jf rt s "$REPO/alice/*" | expect_count 0

step "tokens: rt atc (Artifactory API) then jf atc (Access API) with it"
token=$(jf rt atc "$USER1" --expiry 600 | jq -r .access_token)
[[ -n "$token" && "$token" != null ]]
JFROG_CLI_HOME_DIR="$WORK/tokhome" jf config add tok --url "$SCENARIO_URL" --access-token "$token" --interactive=false
JFROG_CLI_HOME_DIR="$WORK/tokhome" jf atc --expiry 300 | jq -e '.access_token | length > 0' >/dev/null
JFROG_CLI_HOME_DIR="$WORK/tokhome" jf rt s "$REPO/*" | expect_count 0
if JFROG_CLI_HOME_DIR="$WORK/tokhome" jf atc --grant-admin 2>/dev/null; then echo "non-admin minted an admin token" >&2; exit 1; fi

step "delete-only-if-present: group-delete, users-delete"
cleanup
if jf rt udel "$USER1" --quiet 2>/dev/null; then echo "deleting a missing user succeeded" >&2; exit 1; fi
