#!/usr/bin/env bash
# Rebuilds the demo from nothing: stops Relic, deletes its data, starts it and
# runs the seed. The settings in /etc/relic and the vulnerability database stay.
# Run as root.
set -euo pipefail

url=${RELIC_DEMO_URL:-https://demo.relic.build}
here=$(cd "$(dirname "$0")" && pwd)
config=$(mktemp)
trap 'rm -f "$config"' EXIT

systemctl stop relic
find /var/lib/relic -mindepth 1 -maxdepth 1 ! -name grype -exec rm -rf {} +
systemctl start relic

for _ in $(seq 120); do
  curl -fs -o /dev/null "$url/readyz" && break
  sleep 2
done
curl -fsS -o /dev/null "$url/readyz"

password=$(sed -n 's/^RELIC_ADMIN_PASSWORD=//p' /etc/relic/relic.env)
export RELIC_CONFIG=$config
relic auth login "$url" --user admin --password-stdin <<<"$password" >/dev/null
RELIC_TOKEN=$(relic auth token create --description demo-seed --expires-days 1 --scope admin)
export RELIC_TOKEN RELIC_URL=$url
"$here/seed.sh"
