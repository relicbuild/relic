#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v dotnet >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER or dotnet missing" >&2
  exit 0
fi
LOCAL=relic-scenario-nnuget-local
REMOTE=relic-scenario-nnuget-remote
VIRTUAL=relic-scenario-nnuget
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"nuget"},
 {"key":"$REMOTE","rclass":"remote","packageType":"nuget","url":"https://www.nuget.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"nuget","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
export NUGET_PACKAGES="$WORK/nuget-packages" DOTNET_CLI_HOME="$WORK/dotnet-home"
v3="$SCENARIO_TLS_URL/nuget/$VIRTUAL/index.json"
v2="$SCENARIO_TLS_URL/nuget/$VIRTUAL"
nuget_config() { # nuget_config <file> <url> <protocolVersion> [credentials]
  cat >"$1" <<XML
<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources><clear /><add key="relic" value="$2" protocolVersion="$3" /></packageSources>
XML
  if [[ -n "${4:-}" ]]; then
    cat >>"$1" <<XML
  <packageSourceCredentials><relic>
    <add key="Username" value="$SCENARIO_USER" />
    <add key="ClearTextPassword" value="$SCENARIO_PASSWORD" />
  </relic></packageSourceCredentials>
XML
  fi
  echo '</configuration>' >>"$1"
}

step "anonymous restore is refused"
mkdir anon && cd anon
dotnet new console -o . >/dev/null
nuget_config nuget.config "$v3" 3
dotnet add package Newtonsoft.Json --version 13.0.3 --no-restore >/dev/null
if dotnet restore >/dev/null 2>&1; then
  echo "anonymous restore was accepted" >&2
  exit 1
fi
cd ..

step "dotnet pack and push (duplicate refused, --skip-duplicate), then delete"
nuget_config nuget.config "$v3" 3 creds
dotnet new classlib -n Relic.Native -o lib >/dev/null
for v in 1.0.0 1.1.0 1.2.0; do
  dotnet pack lib -c Release -p:PackageVersion="$v" -o out >/dev/null
  dotnet nuget push "out/Relic.Native.$v.nupkg" --source relic --api-key "$SCENARIO_USER:$SCENARIO_PASSWORD"
done
if dotnet nuget push out/Relic.Native.1.0.0.nupkg --source relic --api-key "$SCENARIO_USER:$SCENARIO_PASSWORD" >/dev/null 2>&1; then
  echo "a duplicate push was accepted" >&2
  exit 1
fi
dotnet nuget push out/Relic.Native.1.0.0.nupkg --source relic --api-key "$SCENARIO_USER:$SCENARIO_PASSWORD" --skip-duplicate
dotnet nuget delete Relic.Native 1.2.0 --source relic --api-key "$SCENARIO_USER:$SCENARIO_PASSWORD" --non-interactive
jf rt s "$LOCAL/Relic.Native.*.nupkg" | expect_count 2

step "dotnet add package through the V3 virtual, a lock file, a locked-mode restore"
mkdir app && cd app
dotnet new console -o . >/dev/null
cp ../nuget.config .
sed -i 's#<PropertyGroup>#<PropertyGroup><RestorePackagesWithLockFile>true</RestorePackagesWithLockFile>#' app.csproj
dotnet add package Relic.Native --version 1.1.0
dotnet add package Newtonsoft.Json --version 13.0.3
test -f packages.lock.json
jq -e '.dependencies[][] | select(.resolved == "1.1.0")' packages.lock.json >/dev/null
rm -rf "$NUGET_PACKAGES" obj bin
dotnet restore --locked-mode
dotnet build -c Release --no-restore >/dev/null
cd ..
jf rt s "$REMOTE/Newtonsoft.Json.13.0.3.nupkg" | expect_count 1

step "the same restore over a V2 source"
cd app
nuget_config nuget.config "$v2" 2 creds
rm -rf "$NUGET_PACKAGES" obj bin
dotnet restore --locked-mode
test -f "$NUGET_PACKAGES/relic.native/1.1.0/relic.native.1.1.0.nupkg"
cd ..

step "dotnet package search through the virtual finds upstream packages"
dotnet package search Newtonsoft.Json --take 20 --format json >search.json
jq -e '[.searchResult[].packages[].id] | index("Newtonsoft.Json") != null' search.json >/dev/null || {
  cat search.json >&2
  exit 1
}

step "a symbol package: dotnet nuget push sends the .snupkg beside the package; restore gets the package, the symbol server the PDB"
dotnet pack lib -c Release -p:PackageVersion=1.3.0 --include-symbols -p:SymbolPackageFormat=snupkg -o out >/dev/null
test -f out/Relic.Native.1.3.0.snupkg
dotnet nuget push out/Relic.Native.1.3.0.nupkg --source relic --api-key "$SCENARIO_USER:$SCENARIO_PASSWORD"
jf rt s "$LOCAL/Relic.Native.1.3.0.nupkg" | expect_count 1
jf rt s "$LOCAL/Relic.Native.1.3.0.snupkg" | expect_count 1
mkdir symapp && cd symapp
dotnet new console -o . >/dev/null
cp ../nuget.config .
dotnet add package Relic.Native --version 1.3.0 --no-restore
dotnet restore --no-http-cache
dotnet build -c Release --no-restore >/dev/null
cd ..
key=$(jf rt s "$LOCAL/Relic.Native.1.3.0.snupkg" | jq -r '.[0].props["nuget.symbol.key"][0]')
[[ "$key" == relic.native.pdb/* ]]
curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -o served.pdb "$SCENARIO_TLS_URL/nuget/$VIRTUAL/symbols/$key/relic.native.pdb"
unzip -p out/Relic.Native.1.3.0.snupkg 'lib/*/Relic.Native.pdb' >packed.pdb
cmp served.pdb packed.pdb
