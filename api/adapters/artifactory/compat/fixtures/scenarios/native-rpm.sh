#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]] ||
  ! command -v rpmbuild >/dev/null || ! docker info >/dev/null 2>&1; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD, rpmbuild or a docker daemon missing" >&2
  exit 0
fi
LOCAL=relic-scenario-rpm-local
REMOTE=relic-scenario-rpm-remote
VIRTUAL=relic-scenario-rpm
ALL=("$VIRTUAL" "$LOCAL" "$REMOTE")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$LOCAL","rclass":"local","packageType":"yum"},
 {"key":"$REMOTE","rclass":"remote","packageType":"rpm","url":"https://dl.rockylinux.org/pub/rocky/10/BaseOS/x86_64/os"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"rpm","repositories":"$LOCAL,$REMOTE","defaultDeploymentRepo":"$LOCAL"}]
JSON
jf rt rc repos.json
jf rt curl -s "/api/repositories/$LOCAL" | jq -e '.packageType == "yum"' >/dev/null

step "rpmbuild a package, then jf rt upload it to the repository created as yum"
mkdir -p rpmbuild/SPECS
cat >rpmbuild/SPECS/relic-hello.spec <<'SPEC'
Name: relic-hello
Version: 1.0
Release: 1
Summary: Relic's RPM scenario
License: MIT
BuildArch: noarch

%description
Relic's RPM scenario.

%install
mkdir -p %{buildroot}/usr/bin
printf '#!/bin/sh\necho relic-hello\n' >%{buildroot}/usr/bin/relic-hello
chmod 755 %{buildroot}/usr/bin/relic-hello

%files
/usr/bin/relic-hello
SPEC
rpmbuild -bb --define "_topdir $WORK/rpmbuild" rpmbuild/SPECS/relic-hello.spec >/dev/null
jf rt u rpmbuild/RPMS/noarch/relic-hello-1.0-1.noarch.rpm "$LOCAL/r/"
jf rt curl -sf "/$LOCAL/repodata/repomd.xml" | grep primary >/dev/null

step "dnf installs through the virtual: the local package and one from the remote, with Relic's signature on repodata checked"
key=$(jf rt curl -sf "/$VIRTUAL/repodata/repomd.xml.key")
docker run --rm --network host -e URL="$SCENARIO_URL/artifactory/$VIRTUAL" -e U="$SCENARIO_USER" -e P="$SCENARIO_PASSWORD" \
  -e KEY="$key" rockylinux/rockylinux:10.2 sh -ec '
  printf "%s\n" "$KEY" >/etc/pki/rpm-gpg/RPM-GPG-KEY-relic
  printf "[relic]\nname=Relic\nbaseurl=%s\nusername=%s\npassword=%s\ngpgcheck=0\nrepo_gpgcheck=1\ngpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-relic\n" \
    "$URL" "$U" "$P" >/etc/yum.repos.d/relic.repo
  dnf -y -q --disablerepo="*" --enablerepo=relic install relic-hello tree
  [ "$(relic-hello)" = relic-hello ]
  tree --version'

for r in "${ALL[@]}"; do delete_repo "$r"; done
