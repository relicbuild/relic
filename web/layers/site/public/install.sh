#!/bin/sh
# Installs the latest relic release: curl -fsSL https://relic.build/install.sh | sh
#
#   RELIC_VERSION      a release tag (v0.2.0 or 0.2.0); the latest release by default
#   RELIC_INSTALL_DIR  where the binary goes; ~/.local/bin by default
#   RELIC_REPOSITORY   the GitHub repository URL the releases come from
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

name="relic_${version}_${os}_${arch}"
sums="relic_${version}_checksums.txt"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading relic $version for $os/$arch"
curl -fsSL -o "$tmp/$name.tar.gz" "$repository/releases/download/$version/$name.tar.gz" ||
  fail "download of $name.tar.gz failed"
curl -fsSL -o "$tmp/$sums" "$repository/releases/download/$version/$sums" ||
  fail "download of $sums failed"

expected=$(awk -v f="$name.tar.gz" '$2 == f { print $1 }' "$tmp/$sums")
[ -n "$expected" ] || fail "$sums has no entry for $name.tar.gz"
if command -v sha256sum >/dev/null; then
  actual=$(sha256sum "$tmp/$name.tar.gz" | awk '{ print $1 }')
elif command -v shasum >/dev/null; then
  actual=$(shasum -a 256 "$tmp/$name.tar.gz" | awk '{ print $1 }')
else
  fail "sha256sum or shasum is required to check the download"
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $name.tar.gz"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
install -m 0755 "$tmp/$name/relic" "$install_dir/relic"
echo "Installed $("$install_dir/relic" --version) to $install_dir/relic"

case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to your PATH: export PATH=\"$install_dir:\$PATH\"" ;;
esac

echo "Next: relic server setup runs it as a service; relic auth login <url> connects to a server."
