#!/bin/sh
# Usage: bundle.sh <tag> <output-directory>
# Writes <output-directory>/relic-cloudflare_<tag>.tar.gz: the tracked files of
# deploy/cloudflare, with wrangler.jsonc running the release <tag>.
set -eu

tag=$1
output=$2
name="relic-cloudflare_$tag"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

mkdir -p "$output" "$work/$name"

git ls-files deploy/cloudflare | while IFS= read -r file; do
	relative=${file#deploy/cloudflare/}
	[ "$relative" = bundle.sh ] && continue
	mkdir -p "$work/$name/$(dirname "$relative")"
	cp "$file" "$work/$name/$relative"
done

wrangler="$work/$name/wrangler.jsonc"
grep -q '"RELIC_VERSION": "vX.Y.Z"' "$wrangler" || {
	echo "bundle.sh: wrangler.jsonc has no \"RELIC_VERSION\": \"vX.Y.Z\" to stamp" >&2
	exit 1
}
sed "s/\"RELIC_VERSION\": \"vX.Y.Z\"/\"RELIC_VERSION\": \"$tag\"/" "$wrangler" >"$wrangler.stamped"
mv "$wrangler.stamped" "$wrangler"

tar -czf "$output/$name.tar.gz" -C "$work" "$name"
