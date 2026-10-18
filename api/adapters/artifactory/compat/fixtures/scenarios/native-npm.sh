#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${TLS_CA:-}" ]] || ! command -v npm >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER/TLS_CA or npm missing" >&2
  exit 0
fi
LOCAL=relic-scenario-nnpm-local
REMOTE=relic-scenario-nnpm-npmjs
VIRTUAL=relic-scenario-nnpm
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"npm"},
 {"key":"$REMOTE","rclass":"remote","packageType":"npm","url":"https://registry.npmjs.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"npm","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
export NODE_EXTRA_CA_CERTS="$TLS_CA" npm_config_cache="$WORK/npm-cache" npm_config_update_notifier=false \
  npm_config_fund=false npm_config_audit=false YARN_ENABLE_TELEMETRY=0 YARN_ENABLE_GLOBAL_CACHE=0 \
  YARN_GLOBAL_FOLDER="$WORK/yarn-global" # yarn caches registry metadata there, across projects
registry="$SCENARIO_TLS_URL/npm/$VIRTUAL/"
nerf=${registry#https:}
export npm_config_userconfig="$WORK/npmrc"
printf 'registry=%s\n' "$registry" >"$npm_config_userconfig"

pkg() { # pkg <dir> <name> <version> [dependencies-json]
  mkdir -p "$1"
  printf '{"name":"%s","version":"%s","main":"index.js","license":"MIT","dependencies":%s}\n' "$2" "$3" "${4:-{\}}" >"$1/package.json"
  printf 'module.exports = "%s@%s";\n' "$2" "$3" >"$1/index.js"
}

step "anonymous reads are refused"
if npm view is-number version >/dev/null 2>&1; then
  echo "anonymous npm view was accepted" >&2
  exit 1
fi

step "token login with npm adduser's protocol"
token=$(curl -sf -X PUT -H 'Content-Type: application/json' \
  -d "{\"name\":\"$SCENARIO_USER\",\"password\":\"$SCENARIO_PASSWORD\"}" \
  "${registry}-/user/org.couchdb.user:$SCENARIO_USER" | jq -r .token)
[[ -n "$token" && "$token" != null ]]
printf '%s:_authToken=%s\n' "$nerf" "$token" >>"$npm_config_userconfig"
[[ "$(npm whoami)" == "$SCENARIO_USER" ]]

step "npm publish, duplicate refusal, dist-tags, deprecate"
pkg lib relic-native-lib 1.0.0
pkg scoped @relic-native/ui 2.0.0
(cd lib && npm publish) && (cd scoped && npm publish)
if (cd lib && npm publish) >/dev/null 2>&1; then
  echo "republishing 1.0.0 was accepted" >&2
  exit 1
fi
pkg lib relic-native-lib 1.1.0 && (cd lib && npm publish --tag next)
[[ "$(npm view relic-native-lib dist-tags.latest)" == "1.0.0" ]]
[[ "$(npm view relic-native-lib dist-tags.next)" == "1.1.0" ]]
npm dist-tag add relic-native-lib@1.1.0 latest
[[ "$(npm view relic-native-lib version)" == "1.1.0" ]]
npm dist-tag rm relic-native-lib next
npm view relic-native-lib dist-tags --json | jq -e 'has("next") | not' >/dev/null
npm deprecate relic-native-lib@1.0.0 "use 1.1.0"
[[ "$(npm view relic-native-lib@1.0.0 deprecated)" == "use 1.1.0" ]]
jf rt s "$LOCAL/relic-native-lib/-/*" | expect_count 2

step "npm install and npm ci from the lockfile, public packages through the remote"
pkg app relic-native-app 0.1.0 '{"relic-native-lib":"^1.0.0","@relic-native/ui":"2.0.0","chalk":"4.1.2","minimist":"1.2.5"}'
cd app
npm install
jq -e '.packages["node_modules/chalk"].resolved | startswith("'"$registry"'")' package-lock.json >/dev/null
jq -e '.packages["node_modules/relic-native-lib"].version == "1.1.0"' package-lock.json >/dev/null
rm -rf node_modules "$npm_config_cache"
npm ci
node -e 'console.log(require("relic-native-lib"), require("chalk").green("ok"))' | grep -q "relic-native-lib@1.1.0"
# npm audit goes to registry.npmjs.org's advisory database through the remote.
npm audit --json >audit.json || true
jq -e '.vulnerabilities.minimist.severity == "critical"' audit.json >/dev/null
cd ..
jf rt s "$REMOTE/chalk/-/chalk-4.1.2.tgz" | expect_count 1
jf rt s "$REMOTE/ansi-styles/-/*" | jq -e 'length >= 1' >/dev/null

step "yarn berry: token auth, install, immutable lockfile install, npm publish"
mkdir yapp && cd yapp
printf '{"name":"relic-native-yarn","version":"3.0.0","license":"MIT","packageManager":"yarn@%s","dependencies":{"relic-native-lib":"1.1.0","is-number":"7.0.0"}}\n' "$(yarn --version)" >package.json
cat >.yarnrc.yml <<YAML
nodeLinker: node-modules
enableTelemetry: false
npmRegistryServer: "${registry%/}"
npmAlwaysAuth: true
npmAuthToken: "$token"
npmPreapprovedPackages: ["relic-native-lib"]
YAML
yarn install --no-immutable # CI=true makes yarn immutable by default
grep -q "relic-native-lib@npm:1.1.0" yarn.lock
rm -rf node_modules .yarn/cache
yarn install --immutable
node -e 'require("relic-native-lib"); require("is-number")'
printf 'npmPublishRegistry: "%s"\n' "${registry%/}" >>.yarnrc.yml
yarn npm publish
yarn npm info relic-native-yarn --fields version --json | jq -e '.version == "3.0.0"' >/dev/null
cd ..

step "pnpm: install, frozen-lockfile install, publish"
mkdir papp && cd papp
printf '{"name":"relic-native-pnpm","version":"4.0.0","license":"MIT","dependencies":{"relic-native-lib":"^1.1.0","is-number":"7.0.0"}}\n' >package.json
cp "$npm_config_userconfig" .npmrc
pnpm install
grep -q "relic-native-lib" pnpm-lock.yaml
rm -rf node_modules
pnpm install --frozen-lockfile
pnpm publish --no-git-checks
cd ..
jf rt s "$LOCAL/relic-native-pnpm/-/*" | expect_count 1
jf rt s "$LOCAL/relic-native-yarn/-/*" | expect_count 1
[[ "$(npm view relic-native-pnpm version)" == "4.0.0" ]]
