#!/usr/bin/env bash
# covers: terraform, terraform-config
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" ]] || ! command -v terraform >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL or terraform missing" >&2
  exit 0
fi
LOCAL=relic-scenario-tf-local
delete_repo "$LOCAL"
printf '{"key":"%s","rclass":"local","packageType":"terraform"}\n' "$LOCAL" >repo.json
jf rt repo-create repo.json

step "jf tf publish a module with build info"
mkdir -p modules/network
cat >modules/network/main.tf <<'TF'
variable "name" { default = "relic" }
output "greeting" { value = "hello ${var.name}" }
TF
cd modules
jf tfc --server-id-deploy relic --repo-deploy "$LOCAL"
jf tf p --namespace acme --provider aws --tag v1.0.0 --build-name "$BUILD-tf" --build-number 1
jf rt bp "$BUILD-tf" 1
cd ..
jf rt s "$LOCAL/acme/network/aws/v1.0.0.zip" | expect_count 1

step "terraform init consumes the module from Relic's registry"
host="${SCENARIO_TLS_URL#https://}"
token=$(curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -H 'Content-Type: application/json' \
  -d '{"description":"terraform e2e","expiresInSeconds":"86400"}' "$SCENARIO_TLS_URL/api/relic.v1.SecurityService/CreateToken" | jq -r .accessToken)
# TF_TOKEN_<host>: dots become underscores, dashes double underscores.
export "TF_TOKEN_$(echo "$host" | sed 's/-/__/g; s/\./_/g')=$token"
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

delete_repo "$LOCAL"
