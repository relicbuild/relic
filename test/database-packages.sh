#!/usr/bin/env bash
# Prints the packages whose integration tests use a test database
# (databasetest), the only ones a second database engine changes.
set -euo pipefail
cd "$(dirname "$0")/.."
go list -tags=integration -test -f '{{if .ForTest}}{{.ForTest}} {{join .Deps " "}}{{end}}' ./... |
  awk '/ relic\/internal\/platform\/database\/databasetest( |$)/ {print $1}' | sort -u
