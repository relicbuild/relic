#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
platform="linux_$(dpkg --print-architecture)"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v terraform >/dev/null || ! command -v tofu >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER, terraform or tofu missing" >&2
  exit 0
fi
LOCAL=relic-scenario-ntf-local
HASHI=relic-scenario-ntf-hashicorp
TOFU=relic-scenario-ntf-opentofu
VIRTUAL=relic-scenario-ntf
ALL=("$VIRTUAL" "$LOCAL" "$HASHI" "$TOFU")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"terraform"},
 {"key":"$HASHI","rclass":"remote","packageType":"terraform","url":"https://registry.terraform.io"},
 {"key":"$TOFU","rclass":"remote","packageType":"terraform","url":"https://registry.opentofu.org"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"terraform","repositories":"$LOCAL,$HASHI,$TOFU","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
host=${SCENARIO_TLS_URL#https://}
host=${host%%/*}
api="$SCENARIO_TLS_URL/terraform"
token=$(curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -H 'Content-Type: application/json' \
  -d '{"scope":"applied-permissions/user","expires_in":3600}' "$SCENARIO_TLS_URL/access/api/v1/tokens" | jq -r .access_token)
[[ -n "$token" && "$token" != null ]]
cli_config() { # cli_config <file> <mirror repo>
  cat >"$1" <<HCL
provider_installation {
  network_mirror { url = "$api/$2/providers/" }
}
credentials "$host" { token = "$token" }
HCL
}
cli_config "$WORK/relic.tfrc" "$VIRTUAL"
export TF_CLI_CONFIG_FILE="$WORK/relic.tfrc" TF_IN_AUTOMATION=1 CHECKPOINT_DISABLE=1

step "anonymous mirror requests are refused"
[[ "$(curl -s -o /dev/null -w '%{http_code}' "$api/$VIRTUAL/providers/registry.terraform.io/hashicorp/random/index.json")" == 401 ]]

step "terraform init and apply with providers through the mirror"
mkdir tf && cd tf
cat >main.tf <<'TF'
terraform {
  required_providers {
    random = { source = "hashicorp/random", version = "3.6.3" }
    null   = { source = "hashicorp/null", version = "3.2.3" }
  }
}
resource "random_pet" "name" {}
resource "null_resource" "noop" {}
output "pet" { value = random_pet.name.id }
TF
terraform init -input=false -no-color
terraform apply -auto-approve -input=false -no-color >apply.txt
grep -q "pet = " apply.txt
jf rt s "$HASHI/registry.terraform.io/hashicorp/random/terraform-provider-random_3.6.3_$platform.zip" | expect_count 1

step "a multi-platform lockfile, then an init that only reads it"
terraform providers lock -net-mirror="$api/$VIRTUAL/providers/" -platform="$platform" -platform=darwin_arm64 -no-color
# It downloads each platform's package, so it records h1: hashes for both.
[[ "$(grep -c '"h1:' .terraform.lock.hcl)" -ge 4 ]]
rm -rf .terraform
terraform init -input=false -no-color -lockfile=readonly
cd ..

step "air gap: terraform providers mirror, upload to the local, install from it alone"
mkdir mirrored && cd mirrored
cat >main.tf <<'TF'
terraform {
  required_providers {
    time = { source = "hashicorp/time", version = "0.12.1" }
  }
}
TF
terraform providers mirror -platform="$platform" ../mirror >/dev/null
cd ../mirror
find . -name '*.zip' | while read -r f; do
  curl -sf -H "Authorization: Bearer $token" -T "$f" "$SCENARIO_TLS_URL/api/v1/files/$LOCAL/${f#./}" >/dev/null
done
cd ../mirrored
cli_config "$WORK/local.tfrc" "$LOCAL"
TF_CLI_CONFIG_FILE="$WORK/local.tfrc" terraform init -input=false -no-color
test -d .terraform/providers/registry.terraform.io/hashicorp/time/0.12.1
cd ..

step "a module uploaded with curl, consumed through the module registry"
mkdir -p mod && printf 'variable "name" { default = "relic" }\noutput "greeting" { value = "hello ${var.name}" }\n' >mod/main.tf
(cd mod && zip -q ../network.zip main.tf)
curl -sf -H "Authorization: Bearer $token" -T network.zip "$SCENARIO_TLS_URL/api/v1/files/$LOCAL/acme/network/aws/1.0.0.zip" >/dev/null
mkdir app && cd app
cat >main.tf <<TF
module "network" {
  source  = "$host/${LOCAL}__acme/network/aws"
  version = "1.0.0"
}
output "greeting" { value = module.network.greeting }
TF
terraform init -input=false -no-color
terraform apply -auto-approve -input=false -no-color >apply.txt
grep -q 'greeting = "hello relic"' apply.txt
cd ..

step "a public module through the virtual's remote of registry.terraform.io"
mkdir public && cd public
cat >main.tf <<TF
module "label" {
  source    = "$host/${VIRTUAL}__cloudposse/label/null"
  version   = "0.25.0"
  namespace = "relic"
  name      = "scenario"
}
output "id" { value = module.label.id }
TF
terraform init -input=false -no-color
terraform apply -auto-approve -input=false -no-color >apply.txt
grep -q 'id = "relic-scenario"' apply.txt
cd ..
cached=0
for r in "$HASHI" "$TOFU"; do
  cached=$((cached + $(jf rt s "$r/cloudposse/label/null/0.25.0.zip" | jq length)))
done
[[ $cached -ge 1 ]]

step "OpenTofu init and apply with providers from registry.opentofu.org through the mirror"
mkdir tofu && cd tofu
cat >main.tf <<'TF'
terraform {
  required_providers {
    random = { source = "hashicorp/random", version = "3.6.3" }
  }
}
resource "random_id" "id" { byte_length = 4 }
TF
tofu init -input=false -no-color
tofu apply -auto-approve -input=false -no-color >/dev/null
cd ..
jf rt s "$TOFU/registry.opentofu.org/hashicorp/random/terraform-provider-random_3.6.3_$platform.zip" | expect_count 1
