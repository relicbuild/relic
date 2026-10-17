#!/usr/bin/env bash
# covers: login
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD not set" >&2
  exit 0
fi
JFROG_CLI_HOME_DIR="$WORK/jfhome"
export JFROG_CLI_HOME_DIR
jf config add web --url "$SCENARIO_URL" --user "$SCENARIO_USER" --password "$SCENARIO_PASSWORD" \
  --basic-auth-only --enc-password=false --interactive=false

step "jf login starts a session and polls"
printf 'y\n' | "$JF" login --server-id web >"$WORK/login.log" 2>&1 &
login_pid=$!
trap 'kill "$login_pid" 2>/dev/null || true; rm -rf "$WORK"' EXIT
session=""
for _ in $(seq 50); do
  session=$(grep -o 'jfClientSession=[0-9a-f-]*' "$WORK/login.log" | head -1 | cut -d= -f2 || true)
  [[ -n "$session" ]] && break
  sleep 0.2
done
[[ -n "$session" ]] || { cat "$WORK/login.log" >&2; exit 1; }

step "a user signs in to the UI and approves with the terminal code"
api="$SCENARIO_URL/api/relic.v1.SessionService"
curl -sf -c "$WORK/cookies" -H 'X-Relic-UI: 1' -H 'Content-Type: application/json' \
  -d "{\"username\":\"$SCENARIO_USER\",\"password\":\"$SCENARIO_PASSWORD\"}" "$api/Login" >/dev/null
[[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$WORK/cookies" -H 'X-Relic-UI: 1' -H 'Content-Type: application/json' \
  -d "{\"session\":\"$session\",\"code\":\"zzzz\"}" "$api/ApproveLogin")" == 400 ]] # wrong code is refused
curl -sf -b "$WORK/cookies" -H 'X-Relic-UI: 1' -H 'Content-Type: application/json' \
  -d "{\"session\":\"$session\",\"code\":\"${session: -4}\"}" "$api/ApproveLogin" >/dev/null

step "jf receives tokens and uses them"
wait "$login_pid" || { cat "$WORK/login.log" >&2; exit 1; }
jq -e '.servers[] | select(.serverId=="web") | (.accessToken | length > 0) and (.refreshToken | length > 0)' \
  "$JFROG_CLI_HOME_DIR/jfrog-cli.conf.v6" >/dev/null
[[ "$(jf rt ping --server-id web)" == "OK" ]]
jf rt curl --server-id web -s /api/system/version | jq -e '.version' >/dev/null
