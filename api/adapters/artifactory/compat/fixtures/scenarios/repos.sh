#!/usr/bin/env bash
# covers: rt repo-create, rt repo-update, rt repo-delete, rt replication-create, rt replication-delete
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD not set" >&2
  exit 0
fi
REMOTE=relic-scenario-generic-remote
VIRTUAL=relic-scenario-generic-virtual
for r in "$VIRTUAL" "$REMOTE" "$REPO" "$REPO2"; do delete_repo "$r"; done
for r in "$REPO" "$REPO2"; do create_generic_repo "$r"; done

step "repo-create with an array template (batch API)"
cat >batch.json <<JSON
[{"key":"$REMOTE","rclass":"remote","packageType":"generic","url":"$SCENARIO_URL/artifactory/$REPO",
  "username":"$SCENARIO_USER","password":"$SCENARIO_PASSWORD","hardFail":"true","socketTimeoutMillis":"15000"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"generic","repositories":"$REPO2,$REMOTE","defaultDeploymentRepo":"$REPO2"}]
JSON
jf rt rc batch.json
jf rt curl -s "/api/repositories/$REMOTE" | jq -e '.password == "" and .rclass == "remote"' >/dev/null

step "remote fetch-through, then search and download from the cache"
printf 'upstream\n' >up.txt
jf rt u up.txt "$REPO/dir/" --flat
jf rt s "$REMOTE/*" | expect_count 0
[[ "$(jf rt curl -s "/$REMOTE/dir/up.txt")" == "upstream" ]]
jf rt s "$REMOTE/*" | expect_count 1
jf rt dl "$REMOTE/dir/up.txt" cached/ --flat
cmp cached/up.txt up.txt

step "virtual resolves members in order and deploys to its default repo"
[[ "$(jf rt curl -s "/$VIRTUAL/dir/up.txt")" == "upstream" ]]
printf 'local-wins\n' >shadow.txt
jf rt u shadow.txt "$REPO2/dir/up.txt"
[[ "$(jf rt curl -s "/$VIRTUAL/dir/up.txt")" == "local-wins" ]]
jf rt u up.txt "$VIRTUAL/deployed/" --flat
jf rt s "$REPO2/deployed/*" | expect_count 1

step "repo-update with an array template"
printf '[{"key":"%s","rclass":"virtual","description":"updated"}]\n' "$VIRTUAL" >update.json
jf rt ru update.json
jf rt curl -s "/api/repositories/$VIRTUAL" | jq -e '.description == "updated" and .defaultDeploymentRepo == "'"$REPO2"'"' >/dev/null

step "replication-create (push) and replication-delete"
cat >repl.json <<JSON
{"repoKey":"$REPO","serverId":"relic","targetRepoKey":"$REPO2","cronExp":"0 0 12 * * ?","enabled":"true","syncDeletes":"false"}
JSON
jf rt rplc repl.json
jf rt curl -s "/api/replications/$REPO" | jq -e '.[0].url | endswith("/'"$REPO2"'")' >/dev/null

step "the worker pushes the repository when triggered"
jf rt curl -s -XPOST "/api/replication/execute/$REPO" | jq -e '.message' >/dev/null
for _ in $(seq 60); do
  [[ "$(jf rt curl -s "/$REPO2/dir/up.txt")" == "upstream" ]] && break
  sleep 1
done
[[ "$(jf rt curl -s "/$REPO2/dir/up.txt")" == "upstream" ]]
jf rt curl -s "/api/replication/$REPO" | jq -e '.status == "ok"' >/dev/null
jf rt rpldel "$REPO" --quiet
[[ "$(jf rt curl -s -o /dev/null -w '%{http_code}' "/api/replications/$REPO")" == 404 ]]

for r in "$VIRTUAL" "$REMOTE" "$REPO" "$REPO2"; do delete_repo "$r"; done
