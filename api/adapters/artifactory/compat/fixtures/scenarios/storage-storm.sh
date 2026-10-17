#!/usr/bin/env bash
# covers:
# tier: platform (concurrent writes against PostgreSQL and S3)
# Deploys, deletes, permanent trash deletes, copies and garbage collections
# race on the same blobs: each round uploads four payloads under forty
# paths while the previous round's files are deleted and purged from the
# trash and a garbage collection is queued, then copies them to a second
# repository while another collection runs. Every upload, delete, purge and
# copy succeeds, only the last round is left in the first repository, every
# file downloads with one of the payloads' checksums, and the tier's storage
# check finds no drift.
source "$(dirname "$0")/lib.sh"
: "${SCENARIO_URL:?}" "${SCENARIO_USER:?}" "${SCENARIO_PASSWORD:?}"
art="$SCENARIO_URL/artifactory"
auth=(-u "$SCENARIO_USER:$SCENARIO_PASSWORD")
rounds=8 paths=40

for r in "$REPO" "$REPO2"; do delete_repo "$r"; create_generic_repo "$r"; done

for p in 0 1 2 3; do head -c 262144 /dev/urandom >"payload-$p"; done
sha256sum payload-* | cut -d' ' -f1 >checksums

deploy() {
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' "${auth[@]}" -T "payload-$(($2 % 4))" "$art/$REPO/round-$1/file-$2.bin")"
  [[ "$code" == 201 ]] || { echo "deploy round $1 file $2: HTTP $code" >&2; return 1; }
}

for round in $(seq 1 "$rounds"); do
  step "round $round: deploys racing deletes, trash purges, copies and a garbage collection"
  pids=()
  for i in $(seq 1 "$paths"); do deploy "$round" "$i" & pids+=($!); done
  previous=$((round - 1))
  if [[ $previous -gt 0 ]]; then
    (
      curl -sf -o /dev/null "${auth[@]}" -X DELETE "$art/$REPO/round-$previous/"
      curl -sf -o /dev/null "${auth[@]}" -X DELETE "$SCENARIO_URL/api/v1/trash/$REPO/round-$previous"
    ) &
    pids+=($!)
  fi
  curl -s -o /dev/null "${auth[@]}" -X POST "$SCENARIO_URL/api/v1/garbage-collections" &
  failed=0
  for p in "${pids[@]}"; do wait "$p" || failed=1; done
  wait
  [[ $failed -eq 0 ]] || { echo "a deploy, delete or trash purge failed in round $round" >&2; exit 1; }
  curl -s -o /dev/null "${auth[@]}" -X POST "$SCENARIO_URL/api/v1/garbage-collections" &
  jf rt copy "$REPO/round-$round/(*)" "$REPO2/round-$round/{1}" --flat >/dev/null
  wait
  jf rt s "$REPO2/round-$round/" | expect_count "$paths"
done
jf rt s "$REPO/" | expect_count "$paths"

step "every file left downloads with one of the payloads"
left=0
for r in "$REPO" "$REPO2"; do
  listing=$(jf rt s "$r/")
  while read -r path; do
    sum="$(curl -sf "${auth[@]}" "$art/$path" | sha256sum | cut -d' ' -f1)"
    grep -qx "$sum" checksums || { echo "$path: content does not match any payload" >&2; exit 1; }
    left=$((left + 1))
  done < <(jq -r '.[].path' <<<"$listing")
done
echo "$left files verified"
[[ $left -eq $((paths * (rounds + 1))) ]] || { echo "$left files left, want $((paths * (rounds + 1)))" >&2; exit 1; }

for r in "$REPO" "$REPO2"; do delete_repo "$r"; done
