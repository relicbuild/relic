#!/usr/bin/env bash
# covers: exchange-oidc-token
# A CI job exchanges an OIDC token, signed by an issuer a workload identity
# trusts, for a short-lived token, then uploads with it. The issuer's key is
# given to Relic as an inline JWKS, so no issuer needs to be reachable.
#
# Needs SCENARIO_URL, SCENARIO_USER and SCENARIO_PASSWORD (set by the e2e
# harness); without them the scenario is skipped.
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD not set" >&2
  exit 0
fi

IDENTITY="e2e${SCENARIO_SUFFIX:-}"
ISSUER="https://ci.e2e.invalid"

b64url() { base64 -w0 | tr '+/' '-_' | tr -d '='; }

relic_api() {
  curl -fsS -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -H 'Content-Type: application/json' "$@"
}

step "an issuer key and its JWKS"
openssl genrsa -out key.pem 2048 2>/dev/null
n=$(openssl rsa -in key.pem -noout -modulus | cut -d= -f2 |
  python3 -c 'import base64, sys; print(base64.urlsafe_b64encode(bytes.fromhex(sys.stdin.read().strip())).decode().rstrip("="))')
jwks=$(jq -cn --arg n "$n" '{keys: [{kty: "RSA", n: $n, e: "AQAB", alg: "RS256", use: "sig", kid: "e2e"}]}')

step "a workload identity that trusts the issuer for repositories under e2e/"
jq -n --arg name "$IDENTITY" --arg issuer "$ISSUER" --arg jwks "$jwks" --arg user "$SCENARIO_USER" \
  '{name: $name, enabled: true, issuer: $issuer, audience: "relic", jwks: $jwks,
    rules: [{claims: {repository: "e2e/*"}, user: $user}]}' >identity.json
relic_api -X PUT --data @identity.json "$SCENARIO_URL/api/v1/workload-identities/$IDENTITY" >/dev/null

mint() {
  local now header payload signature
  now=$(date +%s)
  header=$(printf '{"alg":"RS256","kid":"e2e","typ":"JWT"}' | b64url)
  payload=$(jq -cn --arg iss "$ISSUER" --arg repo "$1" --argjson now "$now" \
    '{iss: $iss, aud: "relic", sub: ("repo:" + $repo), repository: $repo, iat: $now, exp: ($now + 600)}' | b64url)
  signature=$(printf '%s.%s' "$header" "$payload" | openssl dgst -sha256 -sign key.pem | b64url)
  printf '%s.%s.%s' "$header" "$payload" "$signature"
}

step "jf exchange-oidc-token"
jf eot "$IDENTITY" "$(mint e2e/app)" --url "$SCENARIO_URL" >exchange.json
token=$(jq -r '.AccessToken // .access_token' exchange.json)
[[ -n "$token" && "$token" != null ]]

step "the exchanged token uploads"
delete_repo "$REPO"
create_generic_repo "$REPO"
make_tree
jf rt u "src/a/*" "$REPO/oidc/" --flat --url "$SCENARIO_URL/artifactory" --access-token "$token"
jf rt s "$REPO/oidc/*" | expect_count 2

step "a token no rule matches is refused"
if jf eot "$IDENTITY" "$(mint other/app)" --url "$SCENARIO_URL" >/dev/null 2>&1; then
  echo "a token for other/app was exchanged" >&2
  exit 1
fi

delete_repo "$REPO"
relic_api -X DELETE "$SCENARIO_URL/api/v1/workload-identities/$IDENTITY" >/dev/null
