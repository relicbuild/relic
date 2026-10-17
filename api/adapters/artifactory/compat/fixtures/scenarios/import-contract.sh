#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER missing" >&2
  exit 0
fi
A=$SCENARIO_URL/artifactory; X=$SCENARIO_URL/xray; M=relic-scenario-import
U=$SCENARIO_USER; P=$SCENARIO_PASSWORD
ALL=()
repos='['
for t in "npm npm https://registry.npmjs.org" "pypi pypi https://files.pythonhosted.org" "go go https://proxy.golang.org" \
  "maven maven https://repo1.maven.org/maven2" "nuget nuget https://www.nuget.org" "cargo cargo https://index.crates.io" "gems gems https://rubygems.org"; do
  set -- $t
  ALL+=("$M-$1-remote" "$M-$1-local")
  repos+="{\"key\":\"$M-$1-remote\",\"rclass\":\"remote\",\"packageType\":\"$2\",\"url\":\"$3\"},{\"key\":\"$M-$1-local\",\"rclass\":\"local\",\"packageType\":\"$2\"},"
done
ALL+=("$M-dockerhub-remote")
repos+="{\"key\":\"$M-dockerhub-remote\",\"rclass\":\"remote\",\"packageType\":\"docker\",\"url\":\"https://registry-1.docker.io\"}]"
for r in "${ALL[@]}"; do delete_repo "$r"; done
printf '%s\n' "$repos" >repos.json
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT

ok=0; bad=0
chk(){ # chk <label> <expected-code-regex> <curl args...>
  local label=$1 want=$2; shift 2
  local code; code=$(curl -s -o "$WORK/response.out" -w '%{http_code}' -u "$U:$P" "$@")
  if [[ $code =~ ^($want)$ ]]; then ok=$((ok+1)); else bad=$((bad+1)); echo "FAIL $code $label :: $(head -c 300 "$WORK/response.out" | tr '\n' ' ')" >&2; fi
}
sha(){ curl -s -u "$U:$P" "$A/api/storage/$1" | jq -r '.checksums.sha256 // empty'; }
chkjq(){ # chkjq <label> <jq expr that must be true>
  if jq -e "$2" "$WORK/response.out" >/dev/null 2>&1; then ok=$((ok+1)); else bad=$((bad+1)); echo "FAIL $1 :: $(head -c 300 "$WORK/response.out" | tr '\n' ' ')" >&2; fi; }
aql(){ curl -s -o "$WORK/response.out" -w '%{http_code}' -u "$U:$P" -X POST -H 'Content-Type: text/plain' "$A/api/search/aql" -d "$1"; }

step "system"
chk "ping" 200 "$A/api/system/ping"
chk "xray version" 200 "$X/api/v1/system/version"; chkjq "xray_version field" '.xray_version|type=="string"'
chk "version" 200 "$A/api/system/version"; chkjq "version field" '.version|type=="string"'

# fetch <fmt> <warm path> <cache rel path> <local rel path>
fetch(){
  local fmt=$1 warm=$2 cache=$3 local=$4 remote=$M-$1-remote
  step "$fmt"
  chk "warm $warm" 200 "$A/$warm"
  chk "storage $remote-cache/$cache" 200 "$A/api/storage/$remote-cache/$cache"; chkjq "sha256" '.checksums.sha256|length==64'
  chk "stream from $remote-cache" 200 "$A/$remote-cache/$cache"
  chk "stream via remote key" 200 "$A/$remote/$cache"
  chk "copy $remote-cache -> $M-$fmt-local" 200 -X POST "$A/api/copy/$remote-cache/$cache?to=$M-$fmt-local%2F${local//\//%2F}"
  local s d; s=$(sha "$remote-cache/$cache"); d=$(sha "$M-$fmt-local/$local")
  if [[ -n $s && $s == "$d" ]]; then ok=$((ok+1)); else bad=$((bad+1)); echo "FAIL copied sha '$d' != src '$s'" >&2; fi
  local dir=${local%/*} name=${local##*/}; [[ $dir == "$local" ]] && dir=.
  code=$(aql "items.find({\"type\":\"any\",\"path\":{\"\$ne\":\".\"},\"\$or\":[{\"\$and\":[{\"repo\":\"$remote-cache\",\"path\":\"${cache%/*}\",\"name\":\"${cache##*/}\"}]}]}).include(\"name\",\"repo\",\"path\",\"actual_md5\",\"actual_sha1\",\"sha256\",\"size\",\"type\",\"modified\",\"created\")")
  [[ $code == 200 ]] && chkjq "aql on -cache finds 1 (repo=$remote-cache)" ".results|length==1 and .[0].repo==\"$remote-cache\"" || { bad=$((bad+1)); echo "FAIL aql $code" >&2; }
}
fetch npm "$M-npm-remote/crelt/-/crelt-1.0.6.tgz" "crelt/-/crelt-1.0.6.tgz" "crelt/-/crelt-1.0.6.tgz"
fetch go "$M-go-remote/golang.org/x/text/@v/v0.14.0.zip" "golang.org/x/text/@v/v0.14.0.zip" "golang.org/x/text/@v/v0.14.0.zip"
fetch maven "$M-maven-remote/org/slf4j/slf4j-api/2.0.5/slf4j-api-2.0.5.jar" "org/slf4j/slf4j-api/2.0.5/slf4j-api-2.0.5.jar" "org/slf4j/slf4j-api/2.0.5/slf4j-api-2.0.5.jar"
fetch gems "$M-gems-remote/gems/rake-13.2.1.gem" "gems/rake-13.2.1.gem" "gems/rake-13.2.1.gem"
fetch cargo "api/cargo/$M-cargo-remote/v1/crates/serde/1.0.210/download" "crates/serde/serde-1.0.210.crate" "crates/serde/serde-1.0.210.crate"

step "pypi"
chk "pypi simple" 200 "$A/api/pypi/$M-pypi-remote/simple/requests/"
href=$(grep -oE 'href="[^"]*requests-2\.31\.0\.tar\.gz[^"]*"' "$WORK/response.out" | head -1 | sed -E 's/href="//; s/"$//; s/#.*//')
abs=$(python3 -c 'import sys,urllib.parse as u;print(u.urljoin(sys.argv[1],sys.argv[2]))' "$A/api/pypi/$M-pypi-remote/simple/requests/" "$href")
[[ $abs == */$M-pypi-remote/* ]] || { bad=$((bad+1)); echo "FAIL pypi resolved href lacks /$M-pypi-remote/: $abs" >&2; }
rel=${abs#*/$M-pypi-remote/}
chk "pypi warm file" 200 "$A/api/pypi/$M-pypi-remote/$rel"
aql "items.find({\"\$or\":[{\"\$and\":[{\"repo\":\"$M-pypi-remote-cache\",\"path\":{\"\$match\":\"*\"},\"name\":{\"\$match\":\"*requests-2.31.0.tar.gz\"}}]}]}).include(\"name\",\"repo\",\"path\",\"actual_md5\",\"actual_sha1\",\"sha256\",\"size\",\"type\",\"modified\",\"created\",\"property\")" >/dev/null
chkjq "aql finds pypi file in -cache" '[.results[]|select(.name=="requests-2.31.0.tar.gz")]|length==1'
pp=$(jq -r '.results[0]|(if .path=="." then "" else .path+"/" end)+.name' "$WORK/response.out")
chk "pypi cache sha" 200 "$A/api/storage/$M-pypi-remote-cache/$pp"
chk "pypi JSON API" 200 "$A/api/pypi/$M-pypi-remote/pypi/requests/2.31.0/json"

step "nuget"
chk "nuget V2 download" 200 -L "$A/api/nuget/$M-nuget-remote/Download/Newtonsoft.Json/13.0.3"
aql "items.find({\"\$or\":[{\"\$and\":[{\"repo\":\"$M-nuget-remote-cache\",\"path\":{\"\$match\":\"*\"},\"name\":{\"\$match\":\"*Newtonsoft.Json.13.0.3.nupkg\"}}]}]}).include(\"name\",\"repo\",\"path\",\"property\")" >/dev/null
chkjq "aql finds Newtonsoft.Json.13.0.3.nupkg (exact case)" '[.results[]|select(.name=="Newtonsoft.Json.13.0.3.nupkg")]|length==1'
chk "nuget FindPackagesById" 200 "$A/api/nuget/$M-nuget-remote/FindPackagesById()?id='Newtonsoft.Json'"; grep -q '<entry' "$WORK/response.out" && ok=$((ok+1)) || { bad=$((bad+1)); echo "FAIL no <entry" >&2; }

step "local repositories after the copy"
chk "npm packument" 200 "$A/api/npm/$M-npm-local/crelt"
chk "go @v/list" 200 "$A/api/go/$M-go-local/golang.org/x/text/@v/list"
chk "maven storage folder" 200 "$A/api/storage/$M-maven-local/org/slf4j/slf4j-api"
chk "cargo storage crates/serde" 200 "$A/api/storage/$M-cargo-local/crates/serde"
chk "gems storage info/rake (compact index)" 200 "$A/api/storage/$M-gems-local/info/rake"
chk "repo root" 200 "$A/api/storage/$M-npm-local"
chk "remote cache root" 200 "$A/api/storage/$M-npm-remote-cache"

step "delete"
aql "items.find({\"type\":\"any\",\"path\":{\"\$ne\":\".\"},\"\$or\":[{\"\$and\":[{\"repo\":\"$M-npm-local\",\"path\":\"crelt/-\",\"name\":\"crelt-1.0.6.tgz\"}]}]}).include(\"name\",\"repo\",\"path\",\"type\")" >/dev/null
chkjq "aql finds local file" '.results|length==1'
chk "delete" 204 -X DELETE "$A/$M-npm-local/crelt/-/crelt-1.0.6.tgz"

step "docker"
D=$M-dockerhub-remote; ACC='Accept: application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json'
chk "index manifest" 200 -H "$ACC" "$A/api/docker/$D/v2/library/nginx/manifests/1.25"
child=$(jq -r '[.manifests[]?|select(.platform.os=="linux" and .platform.architecture=="amd64")][0].digest // empty' "$WORK/response.out")
chk "child manifest" 200 -H "$ACC" "$A/api/docker/$D/v2/library/nginx/manifests/$child"
cfg=$(jq -r '.config.digest' "$WORK/response.out"); layer=$(jq -r '.layers[0].digest' "$WORK/response.out")
chk "config blob" 200 "$A/api/docker/$D/v2/library/nginx/blobs/$cfg"
chk "layer blob" 200 -o /dev/null "$A/api/docker/$D/v2/library/nginx/blobs/$layer"
chk "list.manifest.json in -cache" 200 "$A/api/storage/$D-cache/library/nginx/1.25/list.manifest.json"
chk "blob cached (sha256__ dir)" 200 "$A/api/storage/$D-cache/library/nginx/sha256__${child#sha256:}/sha256__${layer#sha256:}"
chk "child manifest.json under sha256__" 200 "$A/api/storage/$D-cache/library/nginx/sha256__${child#sha256:}/manifest.json"
chk "raw list.manifest.json via remote key" 200 -H "$ACC" "$A/$D/library/nginx/1.25/list.manifest.json"
chk "tags/list" 200 "$A/api/docker/$D/v2/library/nginx/tags/list"

step "xray"
chk "scan/graph" '200|201' -X POST -H 'Content-Type: application/json' "$X/api/v1/scan/graph?scan_type=dependency" -d '{"component_id":"root","nodes":[{"component_id":"npm://lodash:4.17.15"}]}'
sid=$(jq -r '.scan_id // empty' "$WORK/response.out")
chk "scan/graph result" '200|202' "$X/api/v1/scan/graph/$sid?include_vulnerabilities=true&include_licenses=true"
chk "scanArtifact" 200 -X POST -H 'Content-Type: application/json' "$X/api/v1/scanArtifact" -d "{\"componentID\":\"docker://library/nginx:1.25\",\"path\":\"$D-cache/library/nginx/1.25/list.manifest.json\"}"
chk "artifact/status" 200 -X POST -H 'Content-Type: application/json' "$X/api/v1/artifact/status" -d "{\"repo\":\"$D-cache\",\"path\":\"library/nginx/1.25/list.manifest.json\"}"; chkjq "overall.status" '.overall.status|type=="string"'
chk "summary by checksum" 200 -X POST -H 'Content-Type: application/json' "$X/api/v2/summary/artifact" -d "{\"checksums\":[\"${child#sha256:}\"]}"
chk "summary by path" 200 -X POST -H 'Content-Type: application/json' "$X/api/v2/summary/artifact" -d "{\"paths\":[\"default/$D-cache/library/nginx/1.25/list.manifest.json\"]}"
chk "artifacts list" 200 "$X/api/v1/artifacts?repo=$D-cache&num_of_rows=1000&page_num=1&order_by=created&direction=desc&search=library/nginx"; chkjq "data[]" '.data|type=="array"'
echo "import contract: ok=$ok fail=$bad" >&2
[[ $bad == 0 ]]
