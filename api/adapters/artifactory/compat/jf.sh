#!/usr/bin/env bash
set -euo pipefail

JF_VERSION=2.124.0

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)
    platform=linux-amd64
    sha256=5d9040dea5759b776f6961717d6f8633e29af19fb4f7f6e14ae2f1b6fddf6cf2 ;;
  Linux-aarch64 | Linux-arm64)
    platform=linux-arm64
    sha256=2ddb1ce0aefc8902191a3ba3e3ab979998ef575335565c2a1d9c3d141b785adb ;;
  Darwin-arm64)
    platform=mac-arm64
    sha256=2c1f05ef6ae8d4cabafe3732fe0df4d0bc7bb11bedcd48b0d0c128e477dda733 ;;
  *) echo "jf.sh: unsupported platform $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

dir="${XDG_CACHE_HOME:-$HOME/.cache}/relic/jf/$JF_VERSION/$platform"
if [[ ! -x "$dir/jf" ]]; then
  mkdir -p "$dir"
  tmp="$(mktemp "$dir/jf.XXXXXX")"
  trap 'rm -f "$tmp"' EXIT
  curl -fsSL --connect-timeout 30 --max-time 600 -o "$tmp" \
    "https://releases.jfrog.io/artifactory/jfrog-cli/v2-jf/$JF_VERSION/jfrog-cli-$platform/jf"
  if command -v sha256sum >/dev/null; then sum=(sha256sum); else sum=(shasum -a 256); fi
  got="$("${sum[@]}" "$tmp" | cut -d' ' -f1)"
  [[ "$got" == "$sha256" ]] || { echo "jf.sh: $platform download has SHA-256 $got" >&2; exit 1; }
  chmod 0755 "$tmp"
  mv "$tmp" "$dir/jf"
fi
echo "$dir/jf"
