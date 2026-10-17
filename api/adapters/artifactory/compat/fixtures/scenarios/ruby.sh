#!/usr/bin/env bash
# covers: ruby
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" ]] || ! command -v gem >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL or ruby missing" >&2
  exit 0
fi
LOCAL=relic-scenario-gems-local
REMOTE=relic-scenario-gems-remote
VIRTUAL=relic-scenario-gems
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"gems"},
 {"key":"$REMOTE","rclass":"remote","packageType":"gems","url":"https://rubygems.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"gems","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
export GEM_HOME="$WORK/gem-home" GEM_PATH="$WORK/gem-home" BUNDLE_USER_HOME="$WORK/bundle-home" BUNDLE_PATH="$WORK/bundle"
api="$SCENARIO_TLS_URL/artifactory/api/gems/$VIRTUAL"
user=$(printf '%s' "$SCENARIO_USER" | jq -sRr @uri)
password=$(printf '%s' "$SCENARIO_PASSWORD" | jq -sRr @uri)
authed="${api/:\/\//://$user:$password@}/"

step "gem build and gem push through the virtual"
mkdir demo && cd demo
cat >relic_demo.gemspec <<'SPEC'
Gem::Specification.new do |s|
  s.name = "relic_demo"
  s.version = "1.0.0"
  s.summary = "Relic e2e"
  s.authors = ["Relic"]
  s.files = ["lib/relic_demo.rb"]
  s.license = "MIT"
  s.add_dependency "rainbow", ">= 3.0"
end
SPEC
mkdir lib && echo 'module RelicDemo; def self.hi = "relic"; end' >lib/relic_demo.rb
gem build relic_demo.gemspec >/dev/null
GEM_HOST_API_KEY="Basic $(printf '%s:%s' "$SCENARIO_USER" "$SCENARIO_PASSWORD" | base64 -w0)" \
  gem push relic_demo-1.0.0.gem --host "$api"
cd ..
jf rt s "$LOCAL/gems/relic_demo-1.0.0.gem" | expect_count 1

step "gem install resolves it and its rubygems.org dependency"
gem install relic_demo --clear-sources --source "$authed" --no-document
ruby -e 'require "relic_demo"; abort unless RelicDemo.hi == "relic"'

step "bundle install with jf ruby and build info"
mkdir app && cd app
printf 'source "%s"\ngem "relic_demo", "1.0.0"\n' "$api/" >Gemfile
jf ruby bundle install --repo "$VIRTUAL" --server-id relic-tls --build-name "$BUILD-ruby" --build-number 1
jf rt bp "$BUILD-ruby" 1
cd ..
jf rt curl -s "/api/build/$BUILD-ruby/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 2' >/dev/null

for r in "${ALL[@]}"; do delete_repo "$r"; done
