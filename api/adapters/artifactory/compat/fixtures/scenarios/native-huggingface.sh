#!/usr/bin/env bash
# covers:
# huggingface_hub and the hf CLI at Relic's own mount: hf auth login and
# whoami, create_repo (exists afterwards, 409 on a second create without
# exist_ok), a dataset created and pushed to as datasets' push_to_hub does,
# its refs, commits and paths info.
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! python3 -c 'import huggingface_hub' 2>/dev/null ||
  ! command -v hf >/dev/null; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER, huggingface_hub or hf missing" >&2
  exit 0
fi
LOCAL=relic-scenario-nhf-local
delete_repo "$LOCAL"
printf '[{"key":"%s","rclass":"local","packageType":"huggingfaceml"}]\n' "$LOCAL" >repos.json
jf rt rc repos.json
trap 'delete_repo "$LOCAL"; rm -rf "$WORK"' EXIT
token=$(curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" -H 'Content-Type: application/json' \
  -d '{"scope":"applied-permissions/user","expires_in":3600}' "$SCENARIO_URL/access/api/v1/tokens" | jq -r .access_token)
[[ -n "$token" && "$token" != null ]]
export HF_HOME="$WORK/hf-home" HF_HUB_DISABLE_TELEMETRY=1 HF_ENDPOINT="$SCENARIO_URL/huggingface/$LOCAL"

step "hf auth login stores the token; hf auth whoami names the user"
hf auth login --token "$token"
hf auth whoami | tee whoami.txt
grep -q "$SCENARIO_USER" whoami.txt

step "create_repo, push to a new dataset, refs, commits and paths info"
python3 - "$SCENARIO_USER" <<'PY'
import sys
from huggingface_hub import HfApi
from huggingface_hub.errors import HfHubHTTPError

user = sys.argv[1]
api = HfApi()
assert api.whoami()["name"] == user, api.whoami()

api.create_repo("relic/created")
assert api.repo_exists("relic/created")
assert api.model_info("relic/created").id == "relic/created"
try:
    api.create_repo("relic/created")
except HfHubHTTPError as e:
    assert e.response.status_code == 409, e
else:
    raise AssertionError("a second create_repo without exist_ok succeeded")
api.create_repo("relic/created", exist_ok=True)

api.create_repo("relic/dataset", repo_type="dataset")
api.upload_file(
    path_or_fileobj=b"a,b\n1,2\n",
    path_in_repo="data/train.csv",
    repo_id="relic/dataset",
    repo_type="dataset",
)
assert api.file_exists("relic/dataset", "data/train.csv", repo_type="dataset")
refs = api.list_repo_refs("relic/dataset", repo_type="dataset")
assert any(b.name == "main" for b in refs.branches), refs
commits = api.list_repo_commits("relic/dataset", repo_type="dataset")
assert len(commits) >= 2, commits
paths = api.get_paths_info("relic/dataset", ["data/train.csv"], repo_type="dataset")
assert [p.path for p in paths] == ["data/train.csv"], paths
PY
jf rt s "$LOCAL/datasets/relic/dataset/*" | jq -e 'any(.[]; .path | endswith("data/train.csv"))' >/dev/null
