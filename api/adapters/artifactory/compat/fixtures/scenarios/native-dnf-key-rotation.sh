#!/usr/bin/env bash
# covers:
# dnf set up with relic package setup dnf keeps working when the
# repository's signing key is rotated: dnf makecache after the rotation
# refetches the key from what setup wrote. It passes once setup writes
# gpgkey=<baseurl>/repodata/repomd.xml.key; with the file:// key setup
# writes now, the last makecache fails, so the scenario is not in
# required.txt yet.
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]] ||
  ! command -v relic >/dev/null || ! command -v rpmbuild >/dev/null || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD, relic, rpmbuild or a docker daemon missing" >&2
  exit 0
fi
LOCAL=relic-scenario-dnfkey-local
ROTATED=relic-scenario-dnfkey-rotated
REPO_ID="relic-$LOCAL"
CLIENT="relic-scenario-dnfkey-$$"
delete_repo "$LOCAL"
printf '[{"key":"%s","rclass":"local","packageType":"rpm"}]\n' "$LOCAL" >repos.json
jf rt rc repos.json
trap 'docker rm -f "$CLIENT" >/dev/null 2>&1 || true; delete_repo "$LOCAL"; rm -rf "$WORK"' EXIT
unset RELIC_URL RELIC_TOKEN
export RELIC_CONFIG="$WORK/relic.yaml"

step "rpmbuild a package and upload it"
mkdir -p rpmbuild/SPECS
cat >rpmbuild/SPECS/relic-dnfkey.spec <<'SPEC'
Name: relic-dnfkey
Version: 1.0
Release: 1
Summary: Relic's dnf key rotation scenario
License: MIT
BuildArch: noarch

%description
Relic's dnf key rotation scenario.

%install
mkdir -p %{buildroot}/usr/bin
printf '#!/bin/sh\necho relic-dnfkey\n' >%{buildroot}/usr/bin/relic-dnfkey
chmod 755 %{buildroot}/usr/bin/relic-dnfkey

%files
/usr/bin/relic-dnfkey
SPEC
rpmbuild -bb --define "_topdir $WORK/rpmbuild" rpmbuild/SPECS/relic-dnfkey.spec >/dev/null
jf rt u rpmbuild/RPMS/noarch/relic-dnfkey-1.0-1.noarch.rpm "$LOCAL/r/"
jf rt curl -sf "/$LOCAL/repodata/repomd.xml" | grep -q primary

step "relic package setup dnf writes the repository file and its key; a Rocky Linux client installs with them"
printf '%s\n' "$SCENARIO_PASSWORD" | relic auth login "$SCENARIO_URL" --user "$SCENARIO_USER" --password-stdin >/dev/null
relic package setup dnf "$LOCAL" --root "$WORK/rootfs" --write
grep -v '^password=' "$WORK/rootfs/etc/yum.repos.d/$REPO_ID.repo"
docker run -d --name "$CLIENT" --network host rockylinux/rockylinux:10.2 sleep infinity >/dev/null
tar -C "$WORK/rootfs" -cf - etc | docker exec -i "$CLIENT" tar -xf - -C /
dnf_client() { docker exec "$CLIENT" dnf -y -q --disablerepo='*' --enablerepo="$REPO_ID" "$@"; }
dnf_client makecache
dnf_client install relic-dnfkey
[[ "$(docker exec "$CLIENT" relic-dnfkey)" == relic-dnfkey ]]

step "rotate the repository's signing key"
old_key=$(jf rt curl -sf "/$LOCAL/repodata/repomd.xml.key")
jf rt curl -sf -X POST -H 'Content-Type: application/json' -d "{\"primaryKeyPairRef\":\"$ROTATED\"}" "/api/repositories/$LOCAL" >/dev/null
jf rt curl -sf "/api/repositories/$LOCAL" | jq -e --arg k "$ROTATED" '.primaryKeyPairRef == $k' >/dev/null
for _ in $(seq 60); do
  [[ "$(jf rt curl -sf "/$LOCAL/repodata/repomd.xml.key")" != "$old_key" ]] && break
  sleep 1
done
[[ "$(jf rt curl -sf "/$LOCAL/repodata/repomd.xml.key")" != "$old_key" ]]

step "dnf makecache still works after the rotation"
if ! dnf_client makecache --refresh; then
  echo "dnf makecache failed after the key rotation: setup's gpgkey= does not follow the repository's key" >&2
  exit 1
fi
