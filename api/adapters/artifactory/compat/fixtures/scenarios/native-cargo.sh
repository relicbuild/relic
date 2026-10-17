#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v cargo >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER or cargo missing" >&2
  exit 0
fi
LOCAL=relic-scenario-ncargo-local
REMOTE=relic-scenario-ncargo-remote
VIRTUAL=relic-scenario-ncargo
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"cargo"},
 {"key":"$REMOTE","rclass":"remote","packageType":"cargo","url":"https://index.crates.io"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"cargo","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
api="$SCENARIO_TLS_URL/cargo"
token=$(curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -H 'Content-Type: application/json' \
  -d '{"scope":"applied-permissions/user","expires_in":3600}' "$SCENARIO_TLS_URL/access/api/v1/tokens" | jq -r .access_token)
[[ -n "$token" && "$token" != null ]]
export CARGO_HOME="$WORK/cargo-home" CARGO_TERM_COLOR=never CARGO_NET_RETRY=2 CARGO_HTTP_CAINFO=/etc/ssl/certs/ca-certificates.crt
mkdir -p "$CARGO_HOME"
cat >"$CARGO_HOME/config.toml" <<TOML
[registry]
global-credential-providers = ["cargo:token"]
[registries.relic]
index = "sparse+$api/$VIRTUAL/index/"
[registries.relic-remote]
index = "sparse+$api/$REMOTE/index/"
[source.crates-io]
replace-with = "relic-remote"
TOML

step "without a token the registry refuses cargo"
cargo new -q --lib probe
if (cd probe && cargo search --registry relic probe) >/dev/null 2>&1; then
  echo "anonymous cargo search was accepted" >&2
  exit 1
fi

step "cargo login writes the token to credentials.toml"
for r in relic relic-remote; do echo "Bearer $token" | cargo login --registry "$r"; done
grep -q "Bearer" "$CARGO_HOME/credentials.toml"

step "cargo publish (with a crates.io dependency), yank and unyank"
cargo new -q --lib lib && cd lib
sed -i 's/^name = "lib"/name = "relic_native_crate"/; s/^edition = .*/edition = "2021"\ndescription = "relic native e2e"\nlicense = "MIT"\npublish = ["relic"]/' Cargo.toml
printf 'itoa = "=1.0.11"\n' >>Cargo.toml
printf 'pub fn render(n: u32) -> String { itoa::Buffer::new().format(n).to_string() }\n' >src/lib.rs
cargo publish --registry relic --allow-dirty
sed -i 's/^version = "0.1.0"/version = "0.2.0"/' Cargo.toml
cargo publish --registry relic --allow-dirty
cargo yank --registry relic relic_native_crate@0.2.0
cargo yank --registry relic --undo relic_native_crate@0.2.0
cd ..
jf rt s "$LOCAL/crates/relic_native_crate/*" | expect_count 2
jf rt s "$REMOTE-cache/*itoa*" | jq -e 'length >= 1' >/dev/null

step "cargo search and cargo add against the local registry"
cargo search --registry relic relic_native | grep -q 'relic_native_crate = "0.2.0"'
cargo new -q app && cd app
cargo add --registry relic relic_native_crate@0.1.0
grep -q 'relic_native_crate = { version = "0.1.0", registry = "relic" }' Cargo.toml
printf 'fn main() { println!("{}", relic_native_crate::render(42)); }\n' >src/main.rs

step "cargo build, then --locked builds from Cargo.lock"
cargo build -q
test "$(./target/debug/app)" = 42
grep -q 'name = "relic_native_crate"' Cargo.lock
rm -rf target "$CARGO_HOME/registry"
cargo fetch --locked
cargo build -q --locked --offline
test "$(./target/debug/app)" = 42
cd ..
