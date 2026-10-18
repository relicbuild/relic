#!/usr/bin/env bash
# covers: relic MCP server, MCP and skill registries
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD not set" >&2
  exit 0
fi
unset RELIC_URL RELIC_TOKEN
export RELIC_CONFIG="$WORK/relic.yaml"
LOCAL=relic-ai-local${SCENARIO_SUFFIX:-}
SERVERS=relic-ai-servers${SCENARIO_SUFFIX:-}
SKILLS=relic-ai-skills${SCENARIO_SUFFIX:-}
AGENT=relic-ai-agent${SCENARIO_SUFFIX:-}
cat >resources.yaml <<YAML
apiVersion: relic.build/v1
kind: Repository
metadata: {name: $LOCAL}
spec: {class: local, format: generic, description: AI agents scenario}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: $SERVERS}
spec: {class: local, format: mcp, description: AI agents scenario}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: $SKILLS}
spec: {class: local, format: skills, description: AI agents scenario}
---
apiVersion: relic.build/v1
kind: User
metadata: {name: $AGENT}
spec: {email: relic-ai-agent@example.com, password: Relic-agent-pass-1}
---
apiVersion: relic.build/v1
kind: Permission
metadata: {name: $AGENT}
spec:
  rules:
    - resource: repository
      selector: {repositories: [$LOCAL, $SERVERS, $SKILLS]}
      include: ["**"]
      grants: [{user: $AGENT, actions: [read]}]
YAML

printf '%s\n' "$SCENARIO_PASSWORD" | relic auth login "$SCENARIO_URL" --user "$SCENARIO_USER" --password-stdin >/dev/null
relic delete -f resources.yaml --ignore-not-found >/dev/null # a previous run's leftovers
relic apply -f resources.yaml >/dev/null
echo "hello agents" >notes.txt
relic artifact upload notes.txt "$LOCAL/docs/notes.txt" >/dev/null

agent=(-u "$AGENT:Relic-agent-pass-1")

# until_listed waits for the registry to index an uploaded entry.
until_listed() {
  for _ in $(seq 30); do
    relic registry list "$1" -o json | jq -e --arg p "$2" '.. | objects | select(.path? == $p)' >/dev/null && return
    sleep 1
  done
  echo "$1/$2 was never listed" >&2
  return 1
}

step "an MCP server definition is served only once approved"
cat >server.json <<'JSON'
{
  "$schema": "https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json",
  "name": "io.example/weather",
  "description": "Weather forecasts",
  "version": "1.1.0",
  "remotes": [{"type": "streamable-http", "url": "https://weather.example.com/mcp"}]
}
JSON
relic artifact upload server.json "$SERVERS/weather/1.1.0/server.json" >/dev/null
until_listed "$SERVERS" weather/1.1.0/server.json
curl -sf "${agent[@]}" "$SCENARIO_URL/api/mcp/$SERVERS/v0/servers" | jq -e '.servers | length == 0' >/dev/null
relic registry approve "$SERVERS/weather/1.1.0/server.json" >/dev/null
curl -sf "${agent[@]}" "$SCENARIO_URL/api/mcp/$SERVERS/v0/servers" |
  jq -e '[.servers[] | .server.name // .name] == ["io.example/weather"]' >/dev/null

step "a skill bundle is refused to users until approved"
mkdir -p skill
printf -- '---\nname: release-notes\ndescription: Writes release notes\n---\nSummarize the merged changes.\n' >skill/SKILL.md
(cd skill && zip -q ../release-notes.zip SKILL.md)
relic artifact upload release-notes.zip "$SKILLS/release-notes/1.0.0/release-notes.zip" >/dev/null
until_listed "$SKILLS" release-notes/1.0.0/release-notes.zip
skill_url="$SCENARIO_URL/api/v1/files/$SKILLS/release-notes/1.0.0/release-notes.zip"
[[ "$(curl -s -o /dev/null -w '%{http_code}' "${agent[@]}" "$skill_url")" == 4* ]]
relic registry approve "$SKILLS/release-notes/1.0.0/release-notes.zip" >/dev/null
curl -sf "${agent[@]}" -o fetched.zip "$skill_url"
unzip -p fetched.zip SKILL.md | grep -q 'name: release-notes'

step "MCP Inspector lists and calls the tools over HTTP"
token=$(relic auth token create --description "AI agents scenario" --expires-days 1 -o json | jq -r '.accessToken // .token')
[[ -n "$token" && "$token" != null ]]
mcp=(mcp-inspector --cli "$SCENARIO_URL/mcp" --transport http --header "Authorization: Bearer $token")
"${mcp[@]}" --method tools/list | jq -e '[.tools[].name] | length == 7 and index("package_allowed") != null' >/dev/null
"${mcp[@]}" --method tools/call --tool-name search_artifacts --tool-arg name=notes.txt |
  jq -e '.content[0].text | fromjson | [.items[] | .repository + "/" + .path] | index("'"$LOCAL"'/docs/notes.txt") != null' >/dev/null
"${mcp[@]}" --method tools/call --tool-name artifact_details --tool-arg repository="$LOCAL" path=docs/notes.txt |
  jq -e '.isError != true' >/dev/null

step "MCP Inspector runs relic mcp over stdio"
mcp-inspector --cli "$(command -v relic)" mcp -e "RELIC_CONFIG=$RELIC_CONFIG" --method tools/call --tool-name list_repositories |
  jq -e '.content[0].text | contains("'"$SERVERS"'")' >/dev/null

step "an anonymous MCP client is refused"
if mcp-inspector --cli "$SCENARIO_URL/mcp" --transport http --method tools/list >/dev/null 2>&1; then exit 1; fi

relic delete -f resources.yaml >/dev/null
