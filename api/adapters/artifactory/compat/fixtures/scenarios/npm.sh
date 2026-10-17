#!/usr/bin/env bash
# covers: npm, npm install, npm publish, npm-config
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD not set" >&2
  exit 0
fi
LOCAL=relic-scenario-npm-local
REMOTE=relic-scenario-npm-remote
VIRTUAL=relic-scenario-npm
for r in "$VIRTUAL" "$REMOTE" "$LOCAL"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"npm"},
 {"key":"$REMOTE","rclass":"remote","packageType":"npm","url":"https://registry.npmjs.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"npm","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json

registry="$SCENARIO_URL/artifactory/api/npm/$VIRTUAL/"
auth=$(printf '%s:%s' "$SCENARIO_USER" "$SCENARIO_PASSWORD" | base64 -w0)
cat >"$WORK/.npmrc" <<NPMRC
registry=$registry
${registry#http:}:_auth=$auth
${registry#http:}:always-auth=true
NPMRC
export npm_config_userconfig="$WORK/.npmrc" npm_config_cache="$WORK/npm-cache" npm_config_update_notifier=false npm_config_fund=false

step "npm publish (scoped and unscoped) through the virtual repository"
for pkg in relic-scenario-pkg @relic-scenario/ui; do
  dir="$WORK/pub-${pkg//[@\/]/_}"
  mkdir -p "$dir" && cd "$dir"
  printf '{"name":"%s","version":"1.0.0","description":"relic e2e","main":"index.js"}\n' "$pkg" >package.json
  printf 'module.exports = "%s";\n' "$pkg" >index.js
  npm publish --registry "$registry"
done
cd "$WORK"
jf rt s "$LOCAL/*" | expect_count 2
[[ "$(npm view relic-scenario-pkg version)" == "1.0.0" ]]
if npm publish --registry "$registry" --prefix "$WORK/pub-relic-scenario-pkg" 2>/dev/null; then
  echo "republishing a version succeeded" >&2; exit 1
fi

step "npm install from local (via virtual) and from npmjs (via remote)"
mkdir -p "$WORK/app" && cd "$WORK/app"
printf '{"name":"app","version":"0.0.0","private":true}\n' >package.json
npm install relic-scenario-pkg @relic-scenario/ui is-number@7.0.0
[[ "$(node -p 'require("relic-scenario-pkg")')" == "relic-scenario-pkg" ]]
[[ "$(node -p 'require("@relic-scenario/ui")')" == "@relic-scenario/ui" ]]
[[ "$(node -p 'require("is-number")(5)')" == "true" ]]
grep -q "/api/npm/$VIRTUAL/" package-lock.json
jf rt s "$REMOTE/is-number/*" | expect_count 1

step "dist-tags"
npm dist-tag add relic-scenario-pkg@1.0.0 beta
npm dist-tag ls relic-scenario-pkg | grep -q "beta: 1.0.0"

step "jf npm-config, jf npm install and jf npm publish with build info"
rm -f "$WORK/.npmrc"
unset npm_config_userconfig
mkdir -p "$WORK/jfapp" && cd "$WORK/jfapp"
printf '{"name":"relic-scenario-jf","version":"2.0.0","description":"built by jf","main":"index.js","dependencies":{"is-number":"7.0.0"}}\n' >package.json
printf 'module.exports = 2;\n' >index.js
jf npmc --server-id-resolve relic --repo-resolve "$VIRTUAL" --server-id-deploy relic --repo-deploy "$LOCAL"
jf npm install --build-name "$BUILD-npm" --build-number 1
[[ -d node_modules/is-number ]]
jf npm publish --build-name "$BUILD-npm" --build-number 1
jf rt bp "$BUILD-npm" 1
jf rt curl -s "/api/build/$BUILD-npm/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 1' >/dev/null
jf rt curl -s "/api/build/$BUILD-npm/1" | jq -e '.buildInfo.modules[0].artifacts | length == 1' >/dev/null
[[ "$(npm view relic-scenario-jf version --registry "$registry" --userconfig /dev/null "--${registry#http:}:_auth=$auth")" == "2.0.0" ]]
jf rt curl -s "/api/storage/$LOCAL/relic-scenario-jf/-/relic-scenario-jf-2.0.0.tgz?properties" | jq -e '.properties["npm.name"][0] == "relic-scenario-jf"' >/dev/null
jf rt bdi "$BUILD-npm" --max-days 0 >/dev/null 2>&1 || true

cd "$WORK"
for r in "$VIRTUAL" "$REMOTE" "$LOCAL"; do delete_repo "$r"; done
