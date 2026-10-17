#!/usr/bin/env bash
# covers: rt ping
source "$(dirname "$0")/lib.sh"

step "rt ping"
[[ "$(jf rt ping)" == "OK" ]]
