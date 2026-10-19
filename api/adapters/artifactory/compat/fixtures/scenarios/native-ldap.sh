#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ ${SCENARIO_REMOTE_SERVER:-} == true ]]; then
  echo "skipped: a remote server cannot reach the scenario's LDAP server" >&2
  exit 0
fi
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_REGISTRY:-}" || -z "${SCENARIO_USER:-}" ]] || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_URL/SCENARIO_REGISTRY/SCENARIO_USER or a docker daemon missing" >&2
  exit 0
fi
A=$SCENARIO_URL/artifactory
REPO=relic-scenario-ldap
DREPO=relic-scenario-ldap-docker
LDAP_PORT=1389
box=relic-scenario-ldap-$RANDOM
ajf() { JFROG_CLI_HOME_DIR="$WORK/alice-jf" jf "$@"; }
admin() { curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -H 'Content-Type: application/json' "$@"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
login_pid=
cleanup() {
  if [[ -n "$login_pid" ]]; then kill "$login_pid" 2>/dev/null || true; fi
  admin -X DELETE "$SCENARIO_URL/access/api/v1/ldap/groups/scenario-groups" >/dev/null 2>&1 || true
  admin -X DELETE "$SCENARIO_URL/access/api/v1/ldap/settings/scenario" >/dev/null 2>&1 || true
  admin -X DELETE "$A/api/security/groups/ldap-devs" >/dev/null 2>&1 || true
  admin -X DELETE "$A/api/v2/security/permissions/ldap-devs" >/dev/null 2>&1 || true
  for u in alice bob carol; do admin -X DELETE "$A/api/security/users/$u" >/dev/null 2>&1 || true; done
  docker rm -f "$box" >/dev/null 2>&1 || true
  delete_repo "$REPO"; delete_repo "$DREPO"
  rm -rf "$WORK"
}
trap cleanup EXIT
# Start clean: remove what a failed run left behind.
admin -X DELETE "$SCENARIO_URL/access/api/v1/ldap/settings/scenario" >/dev/null 2>&1 || true
admin -X DELETE "$SCENARIO_URL/access/api/v1/ldap/groups/scenario-groups" >/dev/null 2>&1 || true
for u in alice bob carol; do admin -X DELETE "$A/api/security/users/$u" >/dev/null 2>&1 || true; done
delete_repo "$REPO"; delete_repo "$DREPO"

step "an OpenLDAP server with people and groups"
cat >"$WORK/seed.ldif" <<'LDIF'
dn: ou=people,dc=example,dc=org
objectClass: organizationalUnit
ou: people

dn: ou=groups,dc=example,dc=org
objectClass: organizationalUnit
ou: groups

dn: uid=alice,ou=people,dc=example,dc=org
objectClass: inetOrgPerson
uid: alice
cn: alice
sn: alice
mail: alice@example.org
userPassword: Alice-pass-1

dn: uid=bob,ou=people,dc=example,dc=org
objectClass: inetOrgPerson
uid: bob
cn: bob
sn: bob
userPassword: Bob-pass-1

dn: uid=carol,ou=people,dc=example,dc=org
objectClass: inetOrgPerson
uid: carol
cn: carol
sn: carol
userPassword: Carol-pass-1

dn: cn=ldap-devs,ou=groups,dc=example,dc=org
objectClass: groupOfUniqueNames
cn: ldap-devs
uniqueMember: uid=alice,ou=people,dc=example,dc=org
LDIF
# The image's first start sometimes fails adding its bootstrap schemas on a
# busy host and stops the container; start it again when it does.
started=false
for attempt in 1 2 3; do
  docker rm -f "$box" >/dev/null 2>&1 || true
  # slapd sizes tables by the open-file limit, which is huge in recent Docker.
  docker run -d --name "$box" --ulimit nofile=1024:1024 -p "$LDAP_PORT:389" -e LDAP_DOMAIN=example.org -e LDAP_ADMIN_PASSWORD=admin-pass \
    -e LDAP_TLS=false osixia/openldap:1.5.0 >/dev/null
  for _ in $(seq 120); do
    docker logs "$box" 2>&1 | grep -q "First start is done" && { started=true; break; }
    [[ $(docker inspect -f '{{.State.Running}}' "$box") == true ]] || break
    sleep 0.5
  done
  $started && break
  echo "the OpenLDAP server did not start (attempt $attempt)" >&2
  docker logs "$box" 2>&1 | tail -5 >&2
done
$started || exit 1
docker cp "$WORK/seed.ldif" "$box:/tmp/seed.ldif"
for _ in $(seq 60); do
  seeded=0
  docker exec "$box" ldapadd -c -x -H ldap://localhost -D cn=admin,dc=example,dc=org -w admin-pass -f /tmp/seed.ldif >/dev/null 2>&1 || seeded=$?
  ((seeded == 0 || seeded == 68)) && break
  sleep 1
done
# The OpenLDAP image restarts slapd once its first-start setup is done, so
# the seed may land before that restart, or only in part. Relic reuses a
# directory sign-in for five minutes without syncing groups again, so alice
# must not sign in before her group is there: wait until the directory
# lists alice as a member of ldap-devs, seeding again (-c skips entries
# already there).
for i in $(seq 60); do
  found=$(docker exec "$box" ldapsearch -x -H ldap://localhost -D cn=admin,dc=example,dc=org -w admin-pass \
    -b ou=groups,dc=example,dc=org "(&(cn=ldap-devs)(uniqueMember=uid=alice,ou=people,dc=example,dc=org))" dn 2>/dev/null || true)
  [[ $found == *"cn=ldap-devs"* ]] && break
  (( i == 60 )) && { echo "the directory never held alice's group" >&2; docker logs "$box" 2>&1 | tail -20 >&2; exit 1; }
  docker exec "$box" ldapadd -c -x -H ldap://localhost -D cn=admin,dc=example,dc=org -w admin-pass -f /tmp/seed.ldif >/dev/null 2>&1 || true
  sleep 1
done

step "LDAP settings, group settings, a Relic group with read and write"
admin -X POST "$SCENARIO_URL/access/api/v1/ldap/settings" -d '{"key":"scenario","enabled":true,
  "ldap_url":"ldap://relic-e2e-dind:'"$LDAP_PORT"'/dc=example,dc=org","auto_create_user":true,"email_attribute":"mail",
  "search":{"search_filter":"uid={0}","search_base":"ou=people","search_sub_tree":true,"manager_dn":"cn=admin,dc=example,dc=org","manager_password":"admin-pass"}}' >/dev/null
admin -X POST "$SCENARIO_URL/access/api/v1/ldap/groups" -d '{"name":"scenario-groups","enabled_ldap":"scenario","group_base_dn":"ou=groups",
  "group_name_attribute":"cn","group_member_attribute":"uniqueMember","filter":"(objectClass=groupOfUniqueNames)","strategy":"static","sub_tree":true}' >/dev/null
admin "$SCENARIO_URL/access/api/v1/ldap/settings/scenario" | jq -e '.search.manager_password == ""' >/dev/null
cat >repos.json <<JSON
[{"key":"$REPO","rclass":"local","packageType":"generic"},{"key":"$DREPO","rclass":"local","packageType":"docker"}]
JSON
jf rt rc repos.json
admin -X PUT "$A/api/security/groups/ldap-devs" -d '{"name":"ldap-devs"}' >/dev/null
admin -X POST "$A/api/v2/security/permissions/ldap-devs" -d '{"name":"ldap-devs","repo":{"repositories":["'"$REPO"'","'"$DREPO"'"],
  "include-patterns":["**"],"actions":{"groups":{"ldap-devs":["read","write","delete"]}}}}' >/dev/null

# slapd may still be restarting: wait until Relic signs alice in through
# the directory (her first sign-in syncs her groups).
for i in $(seq 60); do
  [[ "$(code -u alice:Alice-pass-1 "$A/api/system/version")" == 200 ]] && break
  (( i == 60 )) && { echo "alice never signed in through the directory" >&2; docker logs "$box" 2>&1 | tail -20 >&2; exit 1; }
  docker exec "$box" ldapadd -c -x -H ldap://localhost -D cn=admin,dc=example,dc=org -w admin-pass -f /tmp/seed.ldif >/dev/null 2>&1 || true
  sleep 1
done

step "jf as a directory user: ping, upload, download"
ajf config add alice --url "$SCENARIO_URL" --user alice --password Alice-pass-1 --basic-auth-only --enc-password=false --interactive=false
[[ "$(ajf rt ping --server-id alice)" == "OK" ]]
echo "from alice" >alice.txt
ajf rt u alice.txt "$REPO/alice/" --server-id alice
ajf rt dl "$REPO/alice/alice.txt" got/ --flat --server-id alice
[[ "$(cat got/alice.txt)" == "from alice" ]]
admin "$A/api/security/users/alice" | jq -e '.realm == "ldap" and .email == "alice@example.org"' >/dev/null

step "access tokens for the directory user (Artifactory API, then Access API with it)"
token=$(ajf rt atc --server-id alice --expiry 600 | jq -r '.access_token')
[[ -n "$token" && "$token" != null ]]
[[ "$(code -H "Authorization: Bearer $token" "$A/$REPO/alice/alice.txt")" == 200 ]]
ajf config add alice-token --url "$SCENARIO_URL" --access-token "$token" --interactive=false
ajf atc --server-id alice-token --expiry 300 | jq -e '.access_token | length > 0' >/dev/null

step "docker login, push and pull as the directory user"
reg=$SCENARIO_REGISTRY
mkdir img && echo ldap >img/f && printf 'FROM scratch\nCOPY f /f\n' >img/Dockerfile
docker build -q -t "$reg/$DREPO/ldap-image:1.0" img >/dev/null
export DOCKER_CONFIG="$WORK/docker-config"
echo Alice-pass-1 | docker login "$reg" -u alice --password-stdin >/dev/null
docker push -q "$reg/$DREPO/ldap-image:1.0" >/dev/null
docker rmi -f "$reg/$DREPO/ldap-image:1.0" >/dev/null
docker pull -q "$reg/$DREPO/ldap-image:1.0" >/dev/null
docker logout "$reg" >/dev/null

step "the UI sign-in and the jf login web flow"
api="$SCENARIO_URL/api/relic.v1.SessionService"
curl -sf -c "$WORK/cookies" -H 'X-Relic-UI: 1' -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"Alice-pass-1"}' "$api/Login" >/dev/null
printf 'y\n' | JFROG_CLI_HOME_DIR="$WORK/alice-jf" "$JF" login --server-id alice >"$WORK/login.log" 2>&1 &
login_pid=$!
session=""
for _ in $(seq 50); do
  session=$(grep -o 'jfClientSession=[0-9a-f-]*' "$WORK/login.log" | head -1 | cut -d= -f2 || true)
  [[ -n "$session" ]] && break
  sleep 0.2
done
[[ -n "$session" ]] || { cat "$WORK/login.log" >&2; exit 1; }
curl -sf -b "$WORK/cookies" -H 'X-Relic-UI: 1' -H 'Content-Type: application/json' \
  -d "{\"session\":\"$session\",\"code\":\"${session: -4}\"}" "$api/ApproveLogin" >/dev/null
wait "$login_pid" || { cat "$WORK/login.log" >&2; exit 1; }
[[ "$(ajf rt ping --server-id alice)" == "OK" ]]

step "a directory user in no Relic group is signed in but cannot read"
[[ "$(code -u bob:Bob-pass-1 "$A/api/system/version")" == 200 ]]
[[ "$(code -u bob:Bob-pass-1 "$A/$REPO/alice/alice.txt")" =~ ^(403|404)$ ]]
[[ "$(code -u bob:wrong-pass "$A/api/system/version")" == 401 ]]

step "an outage refuses directory users; the local admin keeps working"
docker stop "$box" >/dev/null
[[ "$(code -u carol:Carol-pass-1 "$A/api/system/version")" == 401 ]]
[[ "$(code -u "$SCENARIO_USER:$SCENARIO_PASSWORD" "$A/api/system/version")" == 200 ]]
