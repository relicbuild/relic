#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v gem >/dev/null || ! command -v bundle >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER, gem or bundler missing" >&2
  exit 0
fi
LOCAL=relic-scenario-ngems-local
REMOTE=relic-scenario-ngems-remote
VIRTUAL=relic-scenario-ngems
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"gems"},
 {"key":"$REMOTE","rclass":"remote","packageType":"gems","url":"https://rubygems.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"gems","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
export JFROG_CLI_HOME_DIR="${JFROG_CLI_HOME_DIR:-$HOME/.jfrog}"
export HOME="$WORK/home" GEM_HOME="$WORK/gem-home" GEM_PATH="$WORK/gem-home" BUNDLE_PATH="$WORK/bundle"
mkdir -p "$HOME/.gem"
api="$SCENARIO_TLS_URL/gems/$VIRTUAL/"
authed="${api/:\/\//://$SCENARIO_USER:$SCENARIO_PASSWORD@}"

gemspec() { # gemspec <name> <version>: builds <name>-<version>.gem
  mkdir -p "$1/lib"
  cat >"$1/$1.gemspec" <<SPEC
Gem::Specification.new do |s|
  s.name = "$1"
  s.version = "$2"
  s.summary = "Relic native e2e"
  s.authors = ["Relic"]
  s.files = ["lib/$1.rb"]
  s.license = "MIT"
  s.add_dependency "rainbow", "~> 3.1"
end
SPEC
  printf 'module %s; def self.v = "%s"; end\n' "$(echo "$1" | sed -E 's/(^|_)([a-z])/\U\2/g')" "$2" >"$1/lib/$1.rb"
  (cd "$1" && gem build -q "$1.gemspec" >/dev/null && mv "$1-$2.gem" ..)
}

step "anonymous installs are refused"
if gem install rainbow --clear-sources --source "$api" --no-document >/dev/null 2>&1; then
  echo "anonymous gem install was accepted" >&2
  exit 1
fi

step "gem push with ~/.gem/credentials from api_key.yaml"
curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" "${api}api/v1/api_key.yaml" >"$HOME/.gem/credentials"
chmod 600 "$HOME/.gem/credentials"
grep -q ":rubygems_api_key:" "$HOME/.gem/credentials"
# RubyGems 3.x loads :rubygems_api_key: under the key name "rubygems".
for v in 1.0.0 1.1.0; do
  gemspec relic_native "$v"
  gem push "relic_native-$v.gem" --host "${api%/}" --key rubygems
done
jf rt s "$LOCAL/gems/relic_native-*.gem" | expect_count 2

step "gem install and search through the virtual, then gem yank"
gem install relic_native -v 1.1.0 --clear-sources --source "$authed" --no-document
ruby -e 'require "relic_native"; abort unless RelicNative.v == "1.1.0"'
gem search relic_native --remote --clear-sources --source "$authed" | grep -q "relic_native (1.1.0)"
gem yank relic_native -v 1.1.0 --host "${api%/}" --key rubygems
jf rt s "$LOCAL/gems/relic_native-*.gem" | jq -e --arg kept "$LOCAL/gems/relic_native-1.0.0.gem" '[.[].path] == [$kept]' >/dev/null
jf rt s "$REMOTE/gems/rainbow-*.gem" | jq -e 'length >= 1' >/dev/null

step "bundler: per-host credentials, Gemfile.lock, frozen install"
mkdir app && cd app
bundle config set --global "${api}" "$SCENARIO_USER:$SCENARIO_PASSWORD"
cat >Gemfile <<RUBY
source "$api"
gem "relic_native", "1.0.0"
gem "rainbow"
RUBY
bundle install --quiet
grep -q "relic_native (1.0.0)" Gemfile.lock
grep -q "remote: $api" Gemfile.lock
rm -rf "$BUNDLE_PATH"
BUNDLE_FROZEN=true bundle install --quiet
bundle exec ruby -e 'require "relic_native"; require "rainbow"; abort unless RelicNative.v == "1.0.0"'
cd ..
