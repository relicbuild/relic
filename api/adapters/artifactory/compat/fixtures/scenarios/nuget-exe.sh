#!/usr/bin/env bash
# covers: nuget, nuget-config
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER or a docker daemon missing" >&2
  exit 0
fi
REMOTE=relic-scenario-nugetexe-remote
VIRTUAL=relic-scenario-nugetexe
for r in "$VIRTUAL" "$REMOTE"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$REMOTE","rclass":"remote","packageType":"nuget","url":"https://www.nuget.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"nuget","repositories":"$REMOTE"}]
JSON
jf rt rc repos.json
box=relic-scenario-nugetexe-$RANDOM
trap 'docker rm -f "$box" >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT
docker create --name "$box" --network host -e U="$SCENARIO_USER" -e P="$SCENARIO_PASSWORD" \
  -e URL="$SCENARIO_URL" -e VIRTUAL="$VIRTUAL" mono:6.12 sleep 900 >/dev/null
docker cp "$JF" "$box:/tmp/jf"
docker start "$box" >/dev/null

step "jf nuget restore through the virtual, over V3 and V2"
docker exec "$box" bash -ec '
  export CI=true JFROG_CLI_REPORT_USAGE=false
  jf() { /tmp/jf "$@"; }
  jf config add relic --url "$URL" --user "$U" --password "$P" --interactive=false >/dev/null
  mkdir -p /tmp/app && cd /tmp/app
  printf "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<packages>\n  <package id=\"Newtonsoft.Json\" version=\"13.0.3\" targetFramework=\"net48\" />\n</packages>\n" >packages.config
  jf nuget-config --server-id-resolve relic --repo-resolve "$VIRTUAL"
  jf nuget restore packages.config -PackagesDirectory packages -NonInteractive
  test -f packages/Newtonsoft.Json.13.0.3/Newtonsoft.Json.13.0.3.nupkg
  rm -rf packages ~/.nuget/packages ~/.local/share/NuGet
  jf nuget-config --server-id-resolve relic --repo-resolve "$VIRTUAL" --nuget-v2
  jf nuget restore packages.config -PackagesDirectory packages -NonInteractive -NoCache
  test -f packages/Newtonsoft.Json.13.0.3/Newtonsoft.Json.13.0.3.nupkg'
jf rt s "$REMOTE/Newtonsoft.Json.13.0.3.nupkg" | expect_count 1

for r in "$VIRTUAL" "$REMOTE"; do delete_repo "$r"; done
