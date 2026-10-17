#!/usr/bin/env bash
# covers: dotnet, dotnet-config
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" ]] || ! command -v dotnet >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL or dotnet missing" >&2
  exit 0
fi
LOCAL=relic-scenario-nuget-local
REMOTE=relic-scenario-nuget-remote
VIRTUAL=relic-scenario-nuget
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"nuget"},
 {"key":"$REMOTE","rclass":"remote","packageType":"nuget","url":"https://www.nuget.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"nuget","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
export NUGET_PACKAGES="$WORK/nuget-packages" DOTNET_CLI_HOME="$WORK/dotnet-home"
feed="$SCENARIO_TLS_URL/artifactory/api/nuget/v3/$VIRTUAL"

step "dotnet pack and nuget push through the virtual"
dotnet new classlib -n Relic.Demo -o lib >/dev/null
dotnet pack lib -c Release -p:PackageVersion=1.0.0 -o out >/dev/null
dotnet nuget push out/Relic.Demo.1.0.0.nupkg -s "$feed" -k "$SCENARIO_USER:$SCENARIO_PASSWORD"
jf rt s "$LOCAL/Relic.Demo.1.0.0.nupkg" | expect_count 1

step "jf dotnet restore with build info"
dotnet new console -n App -o app >/dev/null
dotnet add app package Relic.Demo --version 1.0.0 --no-restore >/dev/null
dotnet add app package Newtonsoft.Json --version 13.0.3 --no-restore >/dev/null
cd app
jf dotnetc --server-id-resolve relic-tls --repo-resolve "$VIRTUAL"
jf dotnet restore --build-name "$BUILD-dotnet" --build-number 1
jf rt bp "$BUILD-dotnet" 1
cd ..
jf rt curl -s "/api/build/$BUILD-dotnet/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 2' >/dev/null
jf rt s "$REMOTE/Newtonsoft.Json.13.0.3.nupkg" | expect_count 1

step "jf dotnet restore over the NuGet V2 feed"
export NUGET_PACKAGES="$WORK/nuget-packages-v2"
cd app
rm -rf obj
jf dotnetc --server-id-resolve relic-tls --repo-resolve "$VIRTUAL" --nuget-v2
jf dotnet restore
cd ..
test -f "$NUGET_PACKAGES/relic.demo/1.0.0/relic.demo.1.0.0.nupkg"
test -f "$NUGET_PACKAGES/newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg"

for r in "${ALL[@]}"; do delete_repo "$r"; done
