#!/usr/bin/env bash
# The k8s tier: one kind cluster, the Helm chart installed with its default
# dependencies, then everything Kubernetes-specific against it, in order:
#   1. the chart lints and renders
#   2. one helm install: the CloudNativePG operator, its PostgreSQL cluster,
#      SeaweedFS and Relic, serving HTTPS with the tier's certificate
#   3. relic and docker traffic through the NodePort
#   4. Kubernetes pulls from Relic: with imagePullSecrets, refused without;
#      skopeo inspect, list-tags and copy
#   5. a rolling restart keeps serving
#   6. helm upgrade keeps the data, the secrets and the PostgreSQL
#      cluster
#   7. HA: server pods SIGKILLed during parallel uploads
#   8. the backup and restore drill
# TestKubernetes (go test -tags e2e ./test/e2e -tier k8s) runs it in the client image (relic, helm, kind, skopeo),
# against the host's Docker daemon, with the chart mounted at /chart and the
# images to load into kind (relic:k8s and the tools) as archives in /images.
# KEEP=1 leaves the cluster running;
# KIND_NODE_IMAGE replaces the pinned node image (a mirror, an offline copy).
set -euo pipefail
cluster=relic-k8s
node=$cluster-control-plane
port=${RELIC_K8S_PORT:-30080}
kind_node="${KIND_NODE_IMAGE:-kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5}"
work="$(mktemp -d)"
export KUBECONFIG="$work/kubeconfig" RELIC_CONFIG="$work/relic.yaml" DOCKER_CONFIG="$work/docker"
cleanup() {
  [[ "${KEEP:-}" == 1 ]] || kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
step() { printf '\n==> [%ss] %s\n' "$SECONDS" "$*" >&2; }
kubectl() { docker exec -i "$node" kubectl --kubeconfig /etc/kubernetes/admin.conf "$@"; }
dump() {
  kubectl get pods -o wide >&2 || true
  kubectl get events --sort-by=.lastTimestamp >&2 || true
  for p in $(kubectl get pods -o name 2>/dev/null); do
    echo "--- $p" >&2
    kubectl logs "$p" --all-containers --tail=100 >&2 || true
  done
}
cd "$work"
cp -r /chart chart

step "1. the chart lints and renders"
if helm dependency list chart | awk 'NR > 1 && NF && $NF != "ok" {missing = 1} END {exit !missing}'; then
  helm repo add cnpg https://cloudnative-pg.github.io/charts --force-update >/dev/null
  helm repo add seaweedfs https://seaweedfs.github.io/seaweedfs/helm --force-update >/dev/null
  helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update >/dev/null
  helm repo add grafana https://grafana.github.io/helm-charts --force-update >/dev/null
  helm dependency build chart >/dev/null
fi
encryption_key=$(openssl rand -base64 32)
admin_password=$(openssl rand -hex 12)
credentials=(--set credentials.encryptionKey="$encryption_key" --set credentials.adminPassword="$admin_password")
chart_image=(--set image.tag=k8s)
helm lint chart "${credentials[@]}" "${chart_image[@]}" >/dev/null
helm lint chart "${credentials[@]}" "${chart_image[@]}" --set cluster.enabled=false \
  --set cloudnative-pg.enabled=false --set credentials.databaseURL=postgres://lint >/dev/null
if helm template r chart "${chart_image[@]}" >/dev/null 2>&1; then echo "the chart rendered without an encryption key" >&2; exit 1; fi
# Not piped into grep -q: helm would die on the closed pipe (pipefail).
rendered=$(helm template r chart "${credentials[@]}" "${chart_image[@]}")
[[ "$rendered" == *"kind: Cluster"* && "$rendered" == *"name: r-cloudnative-pg"* ]]

step "the server's certificate: ECDSA P-384 from the tier's own certificate authority"
openssl req -x509 -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:P-384 -sha384 -days 7 \
  -subj "/CN=Relic k8s tier certificate authority" -keyout ca.key -out ca.crt \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:0" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
openssl req -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:P-384 -sha384 -subj "/CN=relic" \
  -keyout tls.key -out tls.csr 2>/dev/null
printf '%s\n' "subjectAltName=DNS:localhost,IP:127.0.0.1,DNS:relic,DNS:relic.default,DNS:relic.default.svc,DNS:relic.default.svc.cluster.local" \
  "extendedKeyUsage=serverAuth" "keyUsage=critical,digitalSignature" >tls.ext
openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial -sha384 -days 7 -extfile tls.ext -out leaf.crt 2>/dev/null
cat leaf.crt ca.crt >tls.crt
export CURL_CA_BUNDLE="$work/ca.crt"

step "kind cluster: the NodePort on the host's loopback, containerd reaching Relic over HTTPS"
kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
cat >kind.yaml <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry]
      config_path = "/etc/containerd/certs.d"
nodes:
  - role: control-plane
    extraPortMappings:
      - {containerPort: $port, hostPort: $port, listenAddress: "127.0.0.1"}
YAML
kind create cluster --name "$cluster" --config kind.yaml --image "$kind_node" --kubeconfig "$KUBECONFIG" --wait 180s
docker exec -i "$node" sh -ec "
  mkdir -p '/etc/containerd/certs.d/localhost:$port'
  cat >'/etc/containerd/certs.d/localhost:$port/ca.crt'
  printf 'server = \"https://localhost:$port\"\n\n[host.\"https://localhost:$port\"]\n  capabilities = [\"pull\", \"resolve\"]\n  ca = \"/etc/containerd/certs.d/localhost:$port/ca.crt\"\n' \
    >'/etc/containerd/certs.d/localhost:$port/hosts.toml'" <ca.crt
for archive in /images/*.tar; do kind load image-archive "$archive" --name "$cluster" >/dev/null; done
trap dump ERR
if kubectl get crd clusters.postgresql.cnpg.io >/dev/null 2>&1; then
  echo "the kind cluster already has CloudNativePG's CRDs" >&2
  exit 1
fi

step "2. one helm install: the CloudNativePG operator, its PostgreSQL cluster, SeaweedFS and Relic"
kubectl apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Secret
metadata: {name: relic-tls}
type: kubernetes.io/tls
data:
  tls.crt: $(base64 -w0 tls.crt)
  tls.key: $(base64 -w0 tls.key)
YAML
helm install relic chart --wait --timeout 10m \
  --set tls.existingSecret=relic-tls \
  "${credentials[@]}" \
  --set image.repository=relic --set image.tag=k8s --set image.pullPolicy=Never \
  --set cluster.cluster.instances=1 --set cluster.cluster.storage.size=1Gi \
  --set server.replicas=1 --set worker.vulnerabilityDatabase.persistence=false \
  --set service.type=NodePort --set service.nodePort="$port"
kubectl wait --for=condition=Ready pods --all --timeout=300s >/dev/null
kubectl wait --for=condition=Ready cluster/relic-cluster --timeout=300s >/dev/null
cluster_uid=$(kubectl get cluster relic-cluster -o jsonpath='{.metadata.uid}')
secret() { kubectl get secret relic-credentials -o "jsonpath={.data.$1}" | base64 -d; }
password=$(secret adminPassword)
secrets_key=$(secret encryptionKey)
[[ "$password" == "$admin_password" && "$secrets_key" == "$encryption_key" ]]
url=https://localhost:$port
auth=(-fsS -u "admin:$password")
ready() { for _ in $(seq 60); do curl -fsS "$url/readyz" >/dev/null 2>&1 && return; sleep 2; done; return 1; }

step "3. relic and docker through the NodePort"
ready
echo "$password" | relic auth login "$url" --user admin --password-stdin --ca-file ca.crt >/dev/null
relic apply -f - <<'YAML'
apiVersion: relic.build/v1
kind: Repository
metadata: {name: k8s-local}
spec: {class: local, format: generic}
---
apiVersion: relic.build/v1
kind: Repository
metadata: {name: k8s-docker}
spec: {class: local, format: docker}
YAML
echo "k8s $(date +%s%N)" >file.txt
relic artifact upload file.txt k8s-local/a/file.txt
fetched() { relic artifact download k8s-local/a/file.txt -o - | cmp - file.txt; }
fetched
image=localhost:$port/k8s-docker/curl:1
echo "$password" | docker login "localhost:$port" -u admin --password-stdin >/dev/null
# Its own image ID: kubelet ties an image pulled with a secret to that secret,
# and the HA client below runs curl without one.
mkdir img && echo k8s >img/k8s.txt && printf 'FROM curlimages/curl:8.23.0\nCOPY k8s.txt /k8s.txt\n' >img/Dockerfile
docker build -q -t "$image" img >/dev/null
docker push -q "$image" >/dev/null
docker rmi -f "$image" >/dev/null
docker pull -q "$image" >/dev/null

step "4. Kubernetes pulls from Relic: with imagePullSecrets, refused without; skopeo"
kubectl create secret docker-registry relic-pull --docker-server="localhost:$port" --docker-username=admin --docker-password="$password"
kubectl apply -f - <<YAML
apiVersion: v1
kind: Pod
metadata: {name: pulled}
spec:
  imagePullSecrets: [{name: relic-pull}]
  containers: [{name: app, image: "$image", imagePullPolicy: Always, command: ["sh", "-c", "echo hello-from-relic && sleep 3600"]}]
---
apiVersion: v1
kind: Pod
metadata: {name: anonymous}
spec:
  containers: [{name: app, image: "$image", imagePullPolicy: Always, command: ["sleep", "3600"]}]
YAML
kubectl wait --for=condition=Ready pod/pulled --timeout=180s
kubectl logs pulled | grep -q hello-from-relic
for _ in $(seq 60); do
  reason=$(kubectl get pod anonymous -o jsonpath='{.status.containerStatuses[0].state.waiting.reason}')
  [[ "$reason" == ErrImagePull || "$reason" == ImagePullBackOff ]] && break
  sleep 2
done
[[ "$reason" == ErrImagePull || "$reason" == ImagePullBackOff ]]
kubectl delete pod pulled anonymous --wait=false >/dev/null
sk=(skopeo --override-os linux)
creds=(--creds "admin:$password" --tls-verify=false)
"${sk[@]}" inspect "${creds[@]}" "docker://$image" | jq -e '.Architecture == "amd64"' >/dev/null
"${sk[@]}" list-tags "${creds[@]}" "docker://localhost:$port/k8s-docker/curl" | jq -e '.Tags == ["1"]' >/dev/null
"${sk[@]}" copy --src-creds "admin:$password" --dest-creds "admin:$password" --src-tls-verify=false --dest-tls-verify=false \
  "docker://$image" "docker://localhost:$port/k8s-docker/copied/curl:1"
"${sk[@]}" inspect "${creds[@]}" "docker://localhost:$port/k8s-docker/copied/curl:1" >/dev/null
if "${sk[@]}" inspect --no-creds --tls-verify=false "docker://$image" >/dev/null 2>&1; then
  echo "anonymous skopeo inspect was accepted" >&2
  exit 1
fi

step "5. a rolling restart keeps serving"
kubectl rollout restart deploy/relic-server
kubectl rollout status deploy/relic-server --timeout=180s
ready
fetched

step "6. helm upgrade keeps the data, the secrets and the PostgreSQL cluster"
helm upgrade relic chart --wait --timeout 10m --reuse-values --set server.replicas=2 --set server.temporaryVolumeSize=2Gi
kubectl rollout status deploy/relic-server --timeout=300s >/dev/null
[[ "$(kubectl get deploy relic-server -o jsonpath='{.status.readyReplicas}')" == 2 ]]
[[ "$(secret adminPassword)" == "$password" && "$(secret encryptionKey)" == "$secrets_key" ]]
[[ "$(kubectl get cluster relic-cluster -o jsonpath='{.metadata.uid}')" == "$cluster_uid" ]]
ready
fetched
docker rmi -f "$image" >/dev/null
docker pull -q "$image" >/dev/null
helm history relic | grep -q deployed

step "7. HA: three server replicas, SIGKILLed while four clients upload"
helm upgrade relic chart --wait --timeout 10m --reuse-values --set server.replicas=3 --set server.podDisruptionBudget.enabled=false
kubectl run client --image=curlimages/curl:8.23.0 --image-pull-policy=Never --restart=Never --command -- sleep 3600 >/dev/null
kubectl wait --for=condition=Ready pod/client --timeout=120s >/dev/null
kubectl exec -i client -- sh -c 'cat >/tmp/ca.crt' <ca.crt
curl "${auth[@]}" -X POST "$url/api/v1/repositories" -H 'Content-Type: application/json' \
  -d '{"name":"ha-local","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_GENERIC"}' >/dev/null
kubectl exec -i client -- sh -c 'cat >/tmp/upload.sh' <<SH
#!/bin/sh
worker=\$1; n=\$2
for i in \$(seq "\$n"); do
  f=/tmp/w\$worker-\$i.bin
  dd if=/dev/urandom of="\$f" bs=1M count=\$((4 + i % 12)) 2>/dev/null
  sha=\$(sha256sum "\$f" | cut -d' ' -f1)
  code=\$(curl -s -o /dev/null -w '%{http_code}' --max-time 60 --limit-rate 2M -u 'admin:$password' --cacert /tmp/ca.crt \\
    -H "X-Checksum-Sha256: \$sha" -T "\$f" "https://relic/api/v1/files/ha-local/w\$worker/\$i.bin")
  echo "w\$worker/\$i.bin \$code \$sha" >>/tmp/results
  rm -f "\$f"
done
SH
for w in 1 2 3 4; do kubectl exec client -- sh /tmp/upload.sh "$w" 25 & done
sleep 8
for round in 1 2 3; do
  victim= cid=
  for _ in $(seq 60); do
    victim=$(kubectl get pod -l app.kubernetes.io/component=server --field-selector=status.phase=Running \
      -o jsonpath='{range .items[*]}{.metadata.name} {.status.containerStatuses[0].ready}{"\n"}{end}' |
      awk '$2 == "true" {print $1}' | shuf -n1)
    [[ -z "$victim" ]] || cid=$(docker exec "$node" crictl ps -q --name relic --label "io.kubernetes.pod.name=$victim")
    [[ -z "$cid" ]] || break
    sleep 2
  done
  [[ -n "$cid" ]] || { echo "no running relic container to kill (round $round)" >&2; exit 1; }
  echo "SIGKILL $victim (round $round)" >&2
  pid=$(docker exec "$node" crictl inspect --output go-template --template '{{.info.pid}}' "$cid")
  docker exec "$node" kill -9 "$pid"
  sleep 12
done
wait
restarts=$(kubectl get pod -l app.kubernetes.io/component=server \
  -o jsonpath='{range .items[*]}{.status.containerStatuses[0].restartCount}{"\n"}{end}' | awk '{s+=$1} END {print s}')
[[ "$restarts" -ge 3 ]]
kubectl exec client -- cat /tmp/results >results
total=$(wc -l <results)
ok=$(awk '$2=="201"' results | wc -l)
echo "uploads: $total, acknowledged: $ok, cut off: $((total - ok)), server restarts: $restarts" >&2
[[ "$total" -eq 100 ]]
kubectl rollout status deploy/relic-server --timeout=180s >/dev/null
ready
while read -r path code sha; do
  if [[ "$code" == 201 ]]; then
    [[ "$(curl "${auth[@]}" "$url/api/v1/files/ha-local/$path" | sha256sum | cut -d' ' -f1)" == "$sha" ]] ||
      { echo "acknowledged upload $path does not download intact" >&2; exit 1; }
  else
    status=$(curl -s -D headers -o body -w '%{http_code}' -u "admin:$password" "$url/api/v1/files/ha-local/$path")
    if [[ "$status" == 200 ]]; then
      stored=$(grep -i '^x-checksum-sha256:' headers | tr -d '\r' | cut -d' ' -f2)
      [[ "$(sha256sum body | cut -d' ' -f1)" == "$stored" ]] || { echo "cut-off upload $path is torn" >&2; exit 1; }
    fi
    head -c 3145728 /dev/urandom >retry.bin
    [[ "$(curl -s -o /dev/null -w '%{http_code}' -u "admin:$password" -T retry.bin "$url/api/v1/files/ha-local/$path")" == 201 ]] ||
      { echo "retry of $path failed" >&2; exit 1; }
  fi
done <results

step "8. backup and restore drill"
s3_pod() {
  local name=$1 command=$2
  kubectl delete pod "$name" --ignore-not-found >/dev/null
  kubectl apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: $name}
spec:
  restartPolicy: Never
  volumes: [{name: backup, persistentVolumeClaim: {claimName: backup}}]
  containers:
    - name: aws
      image: alpine/k8s:1.37.1
      imagePullPolicy: Never
      command: ["sh", "-ec", "$command"]
      volumeMounts: [{name: backup, mountPath: /backup}]
      env:
        - {name: AWS_DEFAULT_REGION, value: us-east-1}
        - {name: AWS_ACCESS_KEY_ID, valueFrom: {secretKeyRef: {name: relic-seaweedfs-s3-secret, key: admin_access_key_id}}}
        - {name: AWS_SECRET_ACCESS_KEY, valueFrom: {secretKeyRef: {name: relic-seaweedfs-s3-secret, key: admin_secret_access_key}}}
YAML
  kubectl wait --for=jsonpath='{.status.phase}'=Succeeded "pod/$name" --timeout=300s >/dev/null || { kubectl logs "$name" >&2; return 1; }
}
s3="aws configure set default.s3.addressing_style path && aws --endpoint-url http://relic-seaweedfs-s3:8333 s3"
psql() { kubectl exec -i relic-cluster-1 -c postgres -- "$@"; }
kubectl apply -f - >/dev/null <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: backup}
spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}
YAML
curl "${auth[@]}" -X POST "$url/api/v1/repositories" -H 'Content-Type: application/json' \
  -d '{"name":"drill-local","class":"REPOSITORY_CLASS_LOCAL","format":"FORMAT_GENERIC"}' >/dev/null
mkdir files
for i in $(seq 20); do
  head -c $((RANDOM * 8 + i)) /dev/urandom >"files/f$i.bin"
  curl "${auth[@]}" -T "files/f$i.bin" "$url/api/v1/files/drill-local/set/f$i.bin" >/dev/null
done
curl "${auth[@]}" -X PUT "$url/api/v1/repositories/drill-local/properties/set/f1.bin" -H 'Content-Type: application/json' \
  -d '{"properties":{"drill":{"values":["yes"]}}}' >/dev/null
servers=$(kubectl get deploy relic-server -o jsonpath='{.spec.replicas}')
# Backup: PostgreSQL first, then the artifacts bucket.
psql pg_dump -Fc -d relic >relic.dump
[[ -s relic.dump ]]
s3_pod backup "$s3 sync s3://artifacts /backup/artifacts && find /backup/artifacts -type f | wc -l"
echo after >after.txt
curl "${auth[@]}" -T after.txt "$url/api/v1/files/drill-local/after.txt" >/dev/null
# Disaster: the database and the bucket are lost.
kubectl scale deploy/relic-server deploy/relic-worker --replicas=0 >/dev/null
kubectl wait --for=delete pod -l app.kubernetes.io/component=server --timeout=180s >/dev/null || true
psql psql -v ON_ERROR_STOP=1 -c 'DROP DATABASE relic WITH (FORCE)' -c 'CREATE DATABASE relic OWNER relic' >/dev/null
s3_pod lose "$s3 rm s3://artifacts --recursive >/dev/null"
# Restore: PostgreSQL, then the bucket, then the server and the worker.
psql pg_restore -d relic --no-owner --role=relic <relic.dump
s3_pod restore "$s3 sync /backup/artifacts s3://artifacts"
kubectl scale deploy/relic-server --replicas="$servers" >/dev/null
kubectl rollout status deploy/relic-server --timeout=300s >/dev/null
kubectl scale deploy/relic-worker --replicas=1 >/dev/null
ready
for i in $(seq 20); do
  curl "${auth[@]}" "$url/api/v1/files/drill-local/set/f$i.bin" | cmp - "files/f$i.bin"
done
curl "${auth[@]}" "$url/api/v1/repositories/drill-local/properties/set/f1.bin" | grep -q '"drill"'
[[ "$(curl -s -o /dev/null -w '%{http_code}' -u "admin:$password" "$url/api/v1/files/drill-local/after.txt")" == 404 ]]
echo "k8s tier passed" >&2
