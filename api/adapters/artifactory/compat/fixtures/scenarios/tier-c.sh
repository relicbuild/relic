#!/usr/bin/env bash
# covers: apptrust app-create, apptrust app-update, apptrust app-delete, apptrust app-export, apptrust app-import, apptrust package-bind, apptrust package-unbind, apptrust ping, apptrust version-create, apptrust version-promote, apptrust version-rollback, apptrust version-release, apptrust version-delete, apptrust version-distribute, apptrust version-delete-remote, apptrust version-update, apptrust version-update-sources, ds release-bundle-create, ds release-bundle-update, ds release-bundle-sign, ds release-bundle-distribute, ds release-bundle-delete, mc jpd-add, mc jpd-delete, mc license-acquire, mc license-deploy, mc license-release, pl status, pl sync, pl sync-status, pl trigger, pl version, worker init, worker test-run, worker deploy, worker execute, worker undeploy, worker list, worker add-secret, worker list-event, worker edit-schedule, worker execution-history, rt transfer-config, rt transfer-config-merge, rt transfer-files, rt transfer-plugin-install, rt transfer-settings, exchange-oidc-token, stats, sbom-enrich, rt oc
source "$(dirname "$0")/lib.sh"

# check <outcome> <regex> <args...>: outcome is fail (a non-zero exit is
# required) or any; the output must match regex either way.
check() {
  local outcome=$1 want=$2 out rc
  shift 2
  set +e
  out=$(timeout 90 jf "$@" </dev/null 2>&1)
  rc=$?
  set -e
  if ((rc == 124)); then
    echo "jf $* did not end within 90s" >&2
    exit 1
  fi
  if grep -qE "panic:|goroutine [0-9]+ \[" <<<"$out"; then
    echo "jf $* panicked:" >&2
    echo "$out" >&2
    exit 1
  fi
  if [[ $outcome == fail ]] && ((rc == 0)); then
    echo "jf $* succeeded; expected a clean failure:" >&2
    echo "$out" >&2
    exit 1
  fi
  if ! grep -qiE "$want" <<<"$out"; then
    echo "jf $* (exit $rc) did not say why ($want):" >&2
    echo "$out" >&2
    exit 1
  fi
  echo "jf $* -> $rc: $(grep -iE "$want" <<<"$out" | head -1 | cut -c1-120)" >&2
}

step "AppTrust"
for c in app-create app-update app-delete app-export app-import package-bind package-unbind version-create \
  version-promote version-rollback version-release version-delete version-distribute version-delete-remote \
  version-update version-update-sources; do
  check fail "error|mandatory|required|argument|AppTrust|provide" apptrust "$c" relic-app 1.0.0
done
check fail "AppTrust|provide|authentication|501" apptrust ping

step "Distribution v1"
for c in release-bundle-create release-bundle-update release-bundle-sign release-bundle-distribute release-bundle-delete; do
  check fail "error|Distribution|provide|404|501|argument|spec" ds "$c" relic-bundle 1.0 --quiet
done

step "Mission Control"
check fail "Mission Control|provide|501|404|error" mc jpd-add /dev/null
check fail "Mission Control|provide|501|404|error" mc jpd-delete relic-jpd
check fail "error|argument" mc license-acquire
check fail "error|argument" mc license-deploy
check fail "error|argument" mc license-release

step "Pipelines"
for c in status sync-status version; do
  check fail "Pipelines|provide|501|error" pl "$c"
done
check fail "Pipelines|provide|501|error|argument|mandatory" pl sync relic/repo main
check fail "Pipelines|provide|501|error|argument|mandatory" pl trigger relic-pipeline main

step "Workers"
for c in list list-event; do
  check fail "Workers|provide|501|error" worker "$c"
done
for c in deploy execute undeploy add-secret edit-schedule execution-history test-run; do
  check fail "Workers|provide|501|error|argument|manifest|no such file" worker "$c" relic-worker
done
# init writes a worker project locally.
mkdir -p wk && (cd wk && check any "." worker init BEFORE_DOWNLOAD relic-worker)

step "transfer commands"
check fail "data-transfer|plugin|error" rt transfer-files relic relic
check fail "error|cancel|same|identical" rt transfer-config relic relic
check fail "identical|different|error" rt transfer-config-merge relic relic
check fail "directory|plugin|error" rt transfer-plugin-install relic
# transfer-settings only asks for local settings.
check any "." rt transfer-settings

step "unassigned commands that reach a server"
check fail "error|argument" exchange-oidc-token
check fail "error|argument" stats
check fail "error|does not exist" sbom-enrich relic-missing.json
check fail "error|mandatory|repo" rt oc
