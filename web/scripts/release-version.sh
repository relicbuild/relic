#!/bin/sh
# Prints Relic's newest release tag, pre-releases included.
set -eu
git -c versionsort.suffix=- ls-remote --tags --refs --sort=-v:refname https://github.com/relicbuild/relic 'v*' |
  sed -n '1s|.*refs/tags/||p'
