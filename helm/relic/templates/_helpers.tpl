{{- define "relic.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "relic.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "relic.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
app.kubernetes.io/name: {{ include "relic.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "relic.selector" -}}
app.kubernetes.io/name: {{ include "relic.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "relic.credentialsSecretName" -}}
{{- .Values.existingCredentialsSecret | default (printf "%s-credentials" (include "relic.fullname" .)) -}}
{{- end -}}

{{- define "relic.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "relic.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "relic.image" -}}
{{- $tag := .Values.image.tag -}}
{{- if not $tag -}}
{{- if eq .Chart.AppVersion "0.0.0" -}}
{{- fail "image.tag is required: this chart is not a published release; install oci://ghcr.io/relicbuild/charts/relic, or set image.tag" -}}
{{- end -}}
{{- $tag = printf "v%s" .Chart.AppVersion -}}
{{- end -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{- define "relic.singleNode" -}}
{{- $engine := .Values.database.engine | default "postgresql" -}}
{{- $storage := .Values.objectStorage.type | default "s3" -}}
{{- if not (has $engine (list "postgresql" "sqlite")) -}}
{{- fail (printf "database.engine is %q: postgresql or sqlite" $engine) -}}
{{- end -}}
{{- if not (has $storage (list "s3" "azure" "gcs" "filesystem")) -}}
{{- fail (printf "objectStorage.type is %q: s3, azure, gcs or filesystem" $storage) -}}
{{- end -}}
{{- if and (eq $engine "sqlite") .Values.postgresql.enabled -}}
{{- fail "database.engine sqlite needs postgresql.enabled=false (and postgresql-operator.enabled=false)" -}}
{{- end -}}
{{- if and (ne $storage "s3") .Values.seaweedfs.enabled -}}
{{- fail (printf "objectStorage.type %s needs seaweedfs.enabled=false" $storage) -}}
{{- end -}}
{{- if or (eq $engine "sqlite") (eq $storage "filesystem") -}}
{{- if or (ne (int .Values.server.replicas) 1) .Values.server.autoscaling.enabled -}}
{{- fail "SQLite and the filesystem object store run on one node: server.replicas must be 1, without autoscaling" -}}
{{- end -}}
true
{{- end -}}
{{- end -}}

{{- define "relic.serverPortName" -}}
{{- if .Values.tls.enabled }}https{{ else }}http{{ end -}}
{{- end -}}

{{- define "relic.tlsEnv" -}}
- name: RELIC_TLS_MIN_VERSION
  value: {{ .Values.tls.minVersion | quote }}
{{- if .Values.tls.existingSecret }}
- name: RELIC_TLS_CERTIFICATE_FILE
  value: /etc/relic/tls/tls.crt
- name: RELIC_TLS_KEY_FILE
  value: /etc/relic/tls/tls.key
{{- else }}
- name: RELIC_TLS_DIRECTORY
  value: {{ if include "relic.singleNode" . }}/var/lib/relic/data/tls{{ else }}/etc/relic/tls{{ end }}
{{- $service := include "relic.fullname" . }}
{{- $names := list $service (printf "%s.%s" $service .Release.Namespace) (printf "%s.%s.svc" $service .Release.Namespace) (printf "%s.%s.svc.cluster.local" $service .Release.Namespace) }}
{{- if .Values.ingress.enabled }}
{{- range .Values.ingress.hosts }}
{{- $names = append $names .host }}
{{- end }}
{{- end }}
- name: RELIC_TLS_HOSTNAMES
  value: {{ concat $names .Values.tls.hostnames | uniq | join "," | quote }}
{{- end }}
{{- end -}}

{{- define "relic.env" -}}
- name: RELIC_HTTP_LISTEN_ADDRESS
  value: ":8090"
{{- if eq .Values.database.engine "sqlite" }}
- name: RELIC_DATABASE_URL
  value: sqlite:///var/lib/relic/data/relic.db
{{- else if .Values.postgresql.enabled }}
- name: RELIC_DATABASE_URL
  valueFrom: {secretKeyRef: {name: {{ include "relic.dependencyName" (dict "root" . "chart" "postgresql") }}-app, key: uri}}
{{- else }}
- name: RELIC_DATABASE_URL
  valueFrom: {secretKeyRef: {name: {{ include "relic.credentialsSecretName" . }}, key: databaseURL}}
{{- end }}
{{- if eq .Values.objectStorage.type "filesystem" }}
- name: RELIC_OBJECT_STORAGE_TYPE
  value: filesystem
- name: RELIC_OBJECT_STORAGE_PATH
  value: /var/lib/relic/data/objects
{{- else if .Values.seaweedfs.enabled }}
- name: RELIC_OBJECT_STORAGE_ACCESS_KEY
  valueFrom: {secretKeyRef: {name: {{ include "relic.dependencyName" (dict "root" . "chart" "seaweedfs") }}-s3-secret, key: admin_access_key_id}}
- name: RELIC_OBJECT_STORAGE_SECRET_KEY
  valueFrom: {secretKeyRef: {name: {{ include "relic.dependencyName" (dict "root" . "chart" "seaweedfs") }}-s3-secret, key: admin_secret_access_key}}
{{- else if and (eq .Values.objectStorage.type "s3") (not .Values.objectStorage.defaultCredentials) }}
- name: RELIC_OBJECT_STORAGE_ACCESS_KEY
  valueFrom: {secretKeyRef: {name: {{ include "relic.credentialsSecretName" . }}, key: objectStorageAccessKey}}
- name: RELIC_OBJECT_STORAGE_SECRET_KEY
  valueFrom: {secretKeyRef: {name: {{ include "relic.credentialsSecretName" . }}, key: objectStorageSecretKey}}
{{- else if and (eq .Values.objectStorage.type "azure") (not .Values.objectStorage.defaultCredentials) }}
- name: RELIC_OBJECT_STORAGE_AZURE_CONNECTION_STRING
  valueFrom: {secretKeyRef: {name: {{ include "relic.credentialsSecretName" . }}, key: objectStorageAzureConnectionString}}
{{- end }}
- name: RELIC_ADMIN_PASSWORD
  valueFrom: {secretKeyRef: {name: {{ include "relic.credentialsSecretName" . }}, key: adminPassword, optional: true}}
- name: RELIC_ENCRYPTION_KEY
  valueFrom: {secretKeyRef: {name: {{ include "relic.credentialsSecretName" . }}, key: encryptionKey, optional: true}}
{{- if ne .Values.objectStorage.type "filesystem" }}
- name: RELIC_OBJECT_STORAGE_TYPE
  value: {{ .Values.objectStorage.type | quote }}
- name: RELIC_OBJECT_STORAGE_ENDPOINT
  value: {{ include "relic.objectStorageEndpoint" . | quote }}
{{- with .Values.objectStorage.serverSideEncryption }}
- name: RELIC_OBJECT_STORAGE_SSE
  value: {{ . | quote }}
{{- end }}
{{- with .Values.objectStorage.sseKmsKeyId }}
- name: RELIC_OBJECT_STORAGE_SSE_KMS_KEY_ID
  value: {{ . | quote }}
{{- end }}
{{- with .Values.objectStorage.storageClass }}
- name: RELIC_OBJECT_STORAGE_STORAGE_CLASS
  value: {{ . | quote }}
{{- end }}
- name: RELIC_OBJECT_STORAGE_REGION
  value: {{ .Values.objectStorage.region | quote }}
- name: RELIC_OBJECT_STORAGE_CHECKSUM
  value: {{ .Values.objectStorage.checksum | quote }}
- name: RELIC_OBJECT_STORAGE_ARTIFACTS_BUCKET
  value: {{ .Values.objectStorage.artifactsBucket | quote }}
- name: RELIC_OBJECT_STORAGE_TEMPORARY_BUCKET
  value: {{ .Values.objectStorage.temporaryBucket | quote }}
{{- end }}
- name: RELIC_TRUSTED_PROXIES
  value: {{ .Values.trustedProxies | quote }}
- name: TMPDIR
  value: /tmp
{{- if .Values.ldap.caCertificate }}
- name: RELIC_LDAP_CA_CERTIFICATE_FILE
  value: /etc/relic/ldap/ca.crt
{{- end }}
- name: RELIC_METRICS_LISTEN_ADDRESS
  value: {{ ternary ":8091" "off" .Values.metrics.enabled | quote }}
- name: RELIC_METRICS_REPOSITORY_LABEL
  value: {{ .Values.metrics.repositoryNameLabel | quote }}
- name: RELIC_PROFILING_ENABLED
  value: {{ .Values.metrics.profiling | quote }}
- name: RELIC_LOG_LEVEL
  value: {{ .Values.logging.level | quote }}
- name: RELIC_LOG_FORMAT
  value: {{ .Values.logging.format | quote }}
{{- $otlp := include "relic.otlpEndpoint" . }}
{{- if $otlp }}
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: {{ $otlp | quote }}
- name: OTEL_TRACES_SAMPLER
  value: {{ $.Values.tracing.sampler | quote }}
- name: OTEL_TRACES_SAMPLER_ARG
  value: {{ $.Values.tracing.samplerArg | quote }}
- name: K8S_POD_NAME
  valueFrom: {fieldRef: {fieldPath: metadata.name}}
- name: K8S_NAMESPACE_NAME
  valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
- name: OTEL_RESOURCE_ATTRIBUTES
  value: "k8s.namespace.name=$(K8S_NAMESPACE_NAME),k8s.pod.name=$(K8S_POD_NAME)"
{{- if $.Values.logging.otlp }}
- name: OTEL_LOGS_EXPORTER
  value: otlp
{{- end }}
{{- else if .Values.logging.otlp }}
{{- fail "logging.otlp needs an OTLP endpoint: tracing.otlpEndpoint, or monitoringStack.enabled" }}
{{- end }}
{{- with .Values.extraEnv }}
{{ toYaml . }}
{{- end }}
{{- end -}}


{{- define "relic.dependencyName" -}}
{{- $values := index .root.Values .chart -}}
{{- $name := $values.nameOverride | default .chart -}}
{{- if $values.fullnameOverride -}}
{{- $values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else if contains $name .root.Release.Name -}}
{{- .root.Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .root.Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "relic.otlpEndpoint" -}}
{{- if .Values.tracing.otlpEndpoint -}}
{{- .Values.tracing.otlpEndpoint -}}
{{- else if .Values.monitoringStack.enabled -}}
{{- printf "http://%s:4318" (include "relic.dependencyName" (dict "root" . "chart" "alloy")) -}}
{{- end -}}
{{- end -}}

{{- define "relic.monitorLabels" -}}
{{ include "relic.labels" .root }}
release: {{ .root.Release.Name }}
{{- with .extra }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "relic.objectStorageEndpoint" -}}
{{- if .Values.seaweedfs.enabled -}}
{{- printf "http://%s-s3:%v" (include "relic.dependencyName" (dict "root" . "chart" "seaweedfs")) (.Values.seaweedfs.s3.port | default 8333) -}}
{{- else if eq .Values.objectStorage.type "gcs" -}}
{{- .Values.objectStorage.endpoint -}}
{{- else if and (eq .Values.objectStorage.type "s3") .Values.objectStorage.defaultCredentials -}}
{{- .Values.objectStorage.endpoint -}}
{{- else if and (eq .Values.objectStorage.type "azure") (not .Values.objectStorage.defaultCredentials) -}}
{{- .Values.objectStorage.endpoint -}}
{{- else -}}
{{- required "objectStorage.endpoint (or seaweedfs.enabled) is required" .Values.objectStorage.endpoint -}}
{{- end -}}
{{- end -}}

{{- define "relic.metricsIngress" -}}
{{- if .Values.metrics.enabled -}}
- ports:
    - {port: metrics, protocol: TCP}
  {{- with .Values.metrics.allowScrapesFrom }}
  from: {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end -}}
{{- end -}}
