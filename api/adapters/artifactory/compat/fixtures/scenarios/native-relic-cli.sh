#!/usr/bin/env bash
# covers: relic CLI
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD not set" >&2
  exit 0
fi
# The CLI's own login and contexts, not the runner's token.
unset RELIC_URL RELIC_TOKEN
export RELIC_CONFIG="$WORK/relic.yaml"
LOCAL=relic-cli-local
cat >resources.yaml <<YAML
apiVersion: relic.build/v1
kind: Repository
metadata: {name: $LOCAL}
spec: {class: local, format: generic, description: relic CLI scenario}
---
apiVersion: relic.build/v1
kind: Group
metadata: {name: relic-cli-devs}
spec: {description: relic CLI scenario}
---
apiVersion: relic.build/v1
kind: User
metadata: {name: relic-cli-user}
spec: {email: relic-cli@example.com, password: Relic-cli-pass-1, groups: [relic-cli-devs]}
---
apiVersion: relic.build/v1
kind: Permission
metadata: {name: relic-cli-devs}
spec:
  rules:
    - resource: repository
      selector: {repositories: [$LOCAL]}
      include: ["**"]
      grants: [{group: relic-cli-devs, actions: [read, write]}]
YAML

step "relic auth login saves a token"
printf '%s\n' "$SCENARIO_PASSWORD" | relic auth login "$SCENARIO_URL" --user "$SCENARIO_USER" --password-stdin --context admin
[[ "$(stat -c %a "$RELIC_CONFIG")" == 600 ]]
relic delete -f resources.yaml --ignore-not-found >/dev/null # a previous run's leftovers

step "relic apply creates the resources"
[[ "$(relic apply -f resources.yaml | grep -c '│ created ')" == 4 ]]
diff_out=$(relic diff -f resources.yaml --exit-code)
[[ -z "$diff_out" ]]

step "jf uploads as the user apply created"
JFROG_CLI_HOME_DIR="$WORK/jfhome"
export JFROG_CLI_HOME_DIR
jf config add cli --url "$SCENARIO_URL" --user relic-cli-user --password Relic-cli-pass-1 \
  --basic-auth-only --enc-password=false --interactive=false
echo hello >app.txt
jf rt u app.txt "$LOCAL/app/1.0/" --server-id cli | jq -e '.totals.success == 1' >/dev/null

step "relic get, search and download see it"
relic get repositories -o name | grep -x "$LOCAL" >/dev/null
relic get repository "$LOCAL" -o yaml | grep -q 'description: relic CLI scenario'
[[ "$(relic artifact search app.txt --repository "$LOCAL" -o json | jq -r '.items[] | .repository + "/" + .path')" == "$LOCAL/app/1.0/app.txt" ]]
[[ "$(relic artifact download "$LOCAL/app/1.0/app.txt" -o -)" == hello ]]

step "a second apply changes nothing"
[[ "$(relic apply -f resources.yaml | grep -c '│ unchanged ')" == 4 ]]

step "relic delete removes the resources"
[[ "$(relic delete -f resources.yaml | grep -c '│ deleted ')" == 4 ]]
if relic get repositories -o name | grep -x "$LOCAL" >/dev/null; then exit 1; fi
