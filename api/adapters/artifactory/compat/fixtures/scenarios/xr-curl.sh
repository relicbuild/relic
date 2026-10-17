#!/usr/bin/env bash
# covers: xr curl
source "$(dirname "$0")/lib.sh"
step "ping and version through jf xr curl"
jf xr curl -s -XGET /api/v1/system/ping | jq -e '.status == "pong"'
jf xr curl -s -XGET /api/v1/system/version | jq -e '.xray_version | length > 0'
