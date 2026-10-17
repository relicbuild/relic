#!/usr/bin/env bash
# covers: cargo
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" ]] || ! command -v cargo >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL or cargo missing" >&2
  exit 0
fi
LOCAL=relic-scenario-cargo-local
REMOTE=relic-scenario-cargo-remote
for r in "$LOCAL" "$REMOTE"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"cargo"},
 {"key":"$REMOTE","rclass":"remote","packageType":"cargo","url":"https://index.crates.io"}]
JSON
jf rt rc repos.json
B=relic-scenario-cargo-build
jf rt bdi "$B" --max-builds 0 >/dev/null 2>&1 || true

export CARGO_HOME="$WORK/cargo-home" CARGO_TERM_COLOR=never CARGO_NET_RETRY=2
mkdir -p "$CARGO_HOME"
cat >"$CARGO_HOME/config.toml" <<TOML
[registries.relic-remote]
index = "sparse+$SCENARIO_TLS_URL/artifactory/api/cargo/$REMOTE/index/"
[registries.relic-local]
index = "sparse+$SCENARIO_TLS_URL/artifactory/api/cargo/$LOCAL/index/"
[source.crates-io]
replace-with = "relic-remote"
TOML

step "jf cargo build resolves crates.io through the remote"
cargo new -q --lib lib && cd lib
cat >>Cargo.toml <<'TOML'
itoa = "=1.0.11"
TOML
sed -i 's/^name = "lib"/name = "relic_scenario_crate"/; s/^edition = .*/edition = "2021"\ndescription = "relic e2e"\nlicense = "MIT"\npublish = ["relic-local"]/' Cargo.toml
printf 'pub fn render(n: u32) -> String { itoa::Buffer::new().format(n).to_string() }\n' >src/lib.rs
jf cargo build --server-id relic-tls --build-name "$B" --build-number 1
jf rt s "$REMOTE-cache/*itoa*" | jq -e "length >= 1"

step "jf cargo publish to the local registry with build info"
jf cargo publish --server-id relic-tls --registry relic-local --allow-dirty --build-name "$B" --build-number 1
jf rt s "$LOCAL/crates/relic_scenario_crate/relic_scenario_crate-0.1.0.crate" | expect_count 1
jf rt bp "$B" 1
jf rt curl -s "/api/build/$B/1" | jq -e '[.buildInfo.modules[].artifacts[]?.name] | index("relic_scenario_crate-0.1.0.crate") != null'

step "a consumer builds against the published crate"
cd "$WORK" && cargo new -q app && cd app
printf 'relic_scenario_crate = { version = "0.1.0", registry = "relic-local" }\n' >>Cargo.toml
printf 'fn main() { println!("{}", relic_scenario_crate::render(42)); }\n' >src/main.rs
jf cargo build --server-id relic-tls
test "$(./target/debug/app)" = 42

cd "$WORK"
jf rt bdi "$B" --max-builds 0 >/dev/null 2>&1 || true
for r in "$LOCAL" "$REMOTE"; do delete_repo "$r"; done
