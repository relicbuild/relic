#!/bin/sh
# Installs the latest relic release: curl -fsSL https://relic.build/install.sh | sh
#
#   RELIC_VERSION      a release tag (v0.2.0 or 0.2.0); the latest release by default
#   RELIC_INSTALL_DIR  where the binary goes; ~/.local/bin by default
#   RELIC_REPOSITORY   the GitHub repository URL the releases come from
#   RELIC_INDEXER_PLATFORMS  the os/arch relic-indexer builds a server hands to clients,
#                      separated by spaces; this machine's by default, empty for none
#   RELIC_INDEXER_DIR  where those builds go; share/relic/indexer beside the binary's directory by default
set -eu

repository=${RELIC_REPOSITORY:-https://github.com/relicbuild/relic}
install_dir=${RELIC_INSTALL_DIR:-$HOME/.local/bin}

fail() {
  echo "relic install: $*" >&2
  exit 1
}

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "no release for $(uname -s); download one from $repository/releases" ;;
esac

case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no release for $(uname -m); download one from $repository/releases" ;;
esac

command -v curl >/dev/null || fail "curl is required"

mkdir -p "$install_dir" 2>/dev/null && [ -w "$install_dir" ] ||
  fail "cannot write to $install_dir; set RELIC_INSTALL_DIR to a directory you can write to"

indexer_platforms=${RELIC_INDEXER_PLATFORMS-$os/$arch}
indexer_dir=${RELIC_INDEXER_DIR:-$(dirname "$install_dir")/share/relic/indexer}

if [ -n "$indexer_platforms" ]; then
  mkdir -p "$indexer_dir" 2>/dev/null && [ -w "$indexer_dir" ] ||
    fail "cannot write to $indexer_dir; set RELIC_INDEXER_DIR to a directory you can write to, or RELIC_INDEXER_PLATFORMS= to skip the builds"
fi

version=${RELIC_VERSION:-}
if [ -n "$version" ]; then
  case $version in
    v*) ;;
    *) version=v$version ;;
  esac
  curl -fsSLI -o /dev/null "$repository/releases/tag/$version" ||
    fail "no release $version at $repository/releases"
else
  latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$repository/releases/latest") ||
    fail "cannot reach $repository/releases/latest"
  version=${latest##*/}
  case $version in
    v*) ;;
    *)
      # When a repository has only pre-releases, GitHub redirects
      # /releases/latest to /releases. Fall back to the newest release on that page.
      version=$(curl -fsSL "$repository/releases" |
        awk -F'/releases/tag/' 'NF > 1 { split($2, a, "[\"/]"); if (a[1] ~ /^v/ && !found) { found = a[1] } } END { if (found) print found }')
      case $version in
        v*) ;;
        *) fail "no release found at $repository/releases" ;;
      esac
      ;;
  esac
fi

sha256_of() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$1" | awk '{ print $1 }'
  elif command -v shasum >/dev/null; then
    shasum -a 256 "$1" | awk '{ print $1 }'
  else
    fail "sha256sum or shasum is required to check the download"
  fi
}

expected_sum() {
  awk -v f="$1" '$2 == f { print $1 }' "$tmp/$sums"
}

check_sum() {
  expected=$(expected_sum "$1")
  [ -n "$expected" ] || fail "$sums has no entry for $1"
  actual=$(sha256_of "$tmp/$1")
  [ "$actual" = "$expected" ] || fail "checksum mismatch for $1"
}

name="relic_${version}_${os}_${arch}"
sums="relic_${version}_checksums.txt"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading relic $version for $os/$arch"
curl -fsSL -o "$tmp/$name.tar.gz" "$repository/releases/download/$version/$name.tar.gz" ||
  fail "download of $name.tar.gz failed"
curl -fsSL -o "$tmp/$sums" "$repository/releases/download/$version/$sums" ||
  fail "download of $sums failed"

check_sum "$name.tar.gz"

indexer_builds=
for platform in $indexer_platforms; do
  case $platform in
    */*) ;;
    *) fail "RELIC_INDEXER_PLATFORMS takes os/arch pairs, not $platform" ;;
  esac

  indexer_os=${platform%/*}
  indexer_build="relic-indexer-$indexer_os-${platform#*/}"
  if [ "$indexer_os" = windows ]; then
    indexer_build=$indexer_build.exe
  fi

  if [ -z "$(expected_sum "$indexer_build")" ]; then
    [ -z "${RELIC_INDEXER_PLATFORMS+set}" ] || fail "$sums has no entry for $indexer_build"
    echo "relic install: release $version has no $indexer_build; clients that scan locally need one from the server" >&2
    continue
  fi

  echo "Downloading $indexer_build"
  curl -fsSL -o "$tmp/$indexer_build" "$repository/releases/download/$version/$indexer_build" ||
    fail "download of $indexer_build failed"
  check_sum "$indexer_build"
  indexer_builds="$indexer_builds $indexer_build"
done

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
install -m 0755 "$tmp/$name/relic" "$install_dir/relic"
echo "Installed $("$install_dir/relic" --version) to $install_dir/relic"

for indexer_build in $indexer_builds; do
  install -m 0755 "$tmp/$indexer_build" "$indexer_dir/$indexer_build"
  echo "Installed $indexer_build to $indexer_dir"
done

case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to your PATH: export PATH=\"$install_dir:\$PATH\"" ;;
esac

echo "Next: relic server setup runs it as a service; relic auth login <url> connects to a server."
