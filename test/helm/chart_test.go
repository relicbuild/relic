// Package helm_test renders helm/relic with helm template and checks
// what each set of values produces.
package helm_test

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"
)

const chart = "../../helm/relic"

var cnpg = []string{
	"--api-versions",
	"postgresql.cnpg.io/v1",
}

var promOperator = []string{
	"--api-versions",
	"monitoring.coreos.com/v1",
}

type object struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Labels      map[string]string `yaml:"labels"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Data       map[string]string `yaml:"data"`
	raw        map[string]any
	StringData map[string]string `yaml:"stringData"`
	Spec       struct {
		Template struct {
			Metadata struct {
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"metadata"`
			Spec podSpec `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type podSpec struct {
	InitContainers []container `yaml:"initContainers"`
	Containers     []container `yaml:"containers"`
	Volumes        []struct {
		Name     string `yaml:"name"`
		EmptyDir *struct {
			SizeLimit string `yaml:"sizeLimit"`
		} `yaml:"emptyDir"`
	} `yaml:"volumes"`
}

type container struct {
	Name  string   `yaml:"name"`
	Image string   `yaml:"image"`
	Args  []string `yaml:"args"`
	Ports []struct {
		Name          string `yaml:"name"`
		ContainerPort int    `yaml:"containerPort"`
	} `yaml:"ports"`
	Env []struct {
		Name      string `yaml:"name"`
		Value     string `yaml:"value"`
		ValueFrom struct {
			SecretKeyRef struct {
				Name string `yaml:"name"`
				Key  string `yaml:"key"`
			} `yaml:"secretKeyRef"`
		} `yaml:"valueFrom"`
	} `yaml:"env"`
	VolumeMounts []struct {
		Name      string `yaml:"name"`
		MountPath string `yaml:"mountPath"`
	} `yaml:"volumeMounts"`
	Lifecycle struct {
		PreStop struct {
			Sleep struct {
				Seconds int `yaml:"seconds"`
			} `yaml:"sleep"`
		} `yaml:"preStop"`
	} `yaml:"lifecycle"`
}

func (c container) secretRef(name string) string {
	for _, e := range c.Env {
		if e.Name == name {
			return e.ValueFrom.SecretKeyRef.Name + "/" + e.ValueFrom.SecretKeyRef.Key
		}
	}

	return ""
}

func (c container) env(name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}

	return "", false
}

func dependenciesPresent(t *testing.T, helm string) bool {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), helm, "dependency", "list", chart).Output()
	if err != nil {
		return false
	}

	rows := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(rows) < 2 {
		return false
	}

	for _, row := range rows[1:] {
		fields := strings.Fields(row)
		if len(fields) == 0 || fields[len(fields)-1] != "ok" {
			return false
		}
	}

	return true
}

func render(t *testing.T, args ...string) ([]object, string) {
	t.Helper()

	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}

	depsOnce.Do(func() {
		if dependenciesPresent(t, helm) {
			return
		}

		for name, url := range map[string]string{
			"cnpg":                 "https://cloudnative-pg.github.io/charts",
			"seaweedfs":            "https://seaweedfs.github.io/seaweedfs/helm",
			"prometheus-community": "https://prometheus-community.github.io/helm-charts",
			"grafana":              "https://grafana.github.io/helm-charts",
		} {
			if depsErr = exec.CommandContext(t.Context(), helm, "repo", "add", name, url, "--force-update").
				Run(); depsErr != nil {
				return
			}
		}

		depsErr = exec.CommandContext(t.Context(), helm, "dependency", "build", chart).Run()
	})

	if depsErr != nil {
		t.Fatalf("helm dependency build: %v", depsErr)
	}

	var out, stderr bytes.Buffer

	cmd := exec.CommandContext(t.Context(), helm, append([]string{
		"template",
		"r",
		chart,
		"--set",
		"credentials.encryptionKey=" + testEncryptionKey,
	}, args...)...)

	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, stderr.String()
	}

	var objs []object

	dec := yaml.NewDecoder(&out)

	for {
		var n yaml.Node

		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			break
		}

		var o object
		if err == nil {
			err = n.Decode(&o)
		}

		if err == nil {
			err = n.Decode(&o.raw)
		}

		if err != nil {
			t.Fatalf("decode: %v", err)
		}

		if o.Kind != "" {
			objs = append(objs, o)
		}
	}

	return objs, ""
}

var (
	depsOnce sync.Once
	depsErr  error
)

const testEncryptionKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func find(objs []object, kind, name string) *object {
	for i := range objs {
		if objs[i].Kind == kind && objs[i].Metadata.Name == name {
			return &objs[i]
		}
	}

	return nil
}

func count(objs []object, kind string) int {
	n := 0

	for _, o := range objs {
		if o.Kind == kind {
			n++
		}
	}

	return n
}

var external = []string{
	"--set",
	"postgresql.enabled=false",
	"--set",
	"postgresql-operator.enabled=false",
	"--set",
	"seaweedfs.enabled=false",
	"--set",
	"objectStorage.endpoint=http://s3.storage:8333",
	"--set",
	"credentials.databaseURL=postgres://relic:pw@db:5432/relic",
	"--set",
	"credentials.objectStorageAccessKey=ak",
	"--set",
	"credentials.objectStorageSecretKey=sk",
}

func TestChartNeedsStorage(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{
			"no CloudNativePG operator and none bundled",
			[]string{
				"--set",
				"postgresql-operator.enabled=false",
			},
			"CloudNativePG operator",
		},
		{
			"telemetry without the Prometheus Operator's CRDs",
			[]string{
				"--set",
				"monitoringStack.enabled=true",
			},
			"Prometheus Operator's CRDs",
		},
		{
			"OTLP logs without an endpoint",
			[]string{
				"--set",
				"logging.otlp=true",
			},
			"logging.otlp needs an OTLP endpoint",
		},
		{
			"no database",
			[]string{
				"--set",
				"postgresql.enabled=false",
			},
			"credentials.databaseURL",
		},
		{
			"SQLite with the bundled PostgreSQL",
			[]string{
				"--set",
				"database.engine=sqlite",
			},
			"database.engine sqlite needs postgresql.enabled=false",
		},
		{
			"the filesystem store with the bundled SeaweedFS",
			[]string{
				"--set",
				"objectStorage.type=filesystem",
			},
			"objectStorage.type filesystem needs seaweedfs.enabled=false",
		},
		{
			"Azure Blob Storage with the bundled SeaweedFS",
			[]string{
				"--set",
				"objectStorage.type=azure",
			},
			"objectStorage.type azure needs seaweedfs.enabled=false",
		},
		{
			"two workers on the vulnerability database volume",
			[]string{
				"--set",
				"worker.replicas=2",
			},
			"worker.replicas above 1 needs worker.vulnerabilityDatabase.persistence=false",
		},
		{
			"a single node with two replicas",
			append(slices.Clone(singleNode), "--set", "server.replicas=2"),
			"server.replicas must be 1",
		},
		{
			"a single node with autoscaling",
			append(slices.Clone(singleNode), "--set", "server.autoscaling.enabled=true"),
			"without autoscaling",
		},
		{
			"an unknown engine",
			[]string{
				"--set",
				"database.engine=mysql",
			},
			`database.engine is "mysql"`,
		},
		{
			"no S3 endpoint",
			[]string{
				"--set",
				"postgresql.enabled=false",
				"--set",
				"seaweedfs.enabled=false",
				"--set",
				"credentials.databaseURL=x",
				"--set",
				"credentials.objectStorageAccessKey=a",
				"--set",
				"credentials.objectStorageSecretKey=b",
			},
			"objectStorage.endpoint",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, stderr := render(t, tt.args...); !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want %q", stderr, tt.want)
			}
		})
	}
}

var singleNode = []string{
	"--set",
	"database.engine=sqlite",
	"--set",
	"objectStorage.type=filesystem",
	"--set",
	"postgresql.enabled=false",
	"--set",
	"postgresql-operator.enabled=false",
	"--set",
	"seaweedfs.enabled=false",
	"--set",
	"server.replicas=1",
}

var releaseImage = regexp.MustCompile(`^ghcr\.io/relicbuild/relic:v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)

func TestChartSingleNode(t *testing.T) {
	t.Parallel()

	objs, stderr := render(t, singleNode...)
	if stderr != "" {
		t.Fatal(stderr)
	}

	if n := count(objs, "Deployment"); n != 1 {
		t.Errorf("%d Deployments, want the server's only", n)
	}

	for _, kind := range []string{
		"PodDisruptionBudget",
		"HorizontalPodAutoscaler",
		"StatefulSet",
	} {
		if n := count(objs, kind); n != 0 {
			t.Errorf("%d %s", n, kind)
		}
	}

	data := find(objs, "PersistentVolumeClaim", "r-relic-data")
	if data == nil || data.Metadata.Annotations["helm.sh/resource-policy"] != "keep" {
		t.Errorf("the data volume claim = %+v, want one kept on uninstall", data)
	}

	server := find(objs, "Deployment", "r-relic-server")
	if server == nil {
		t.Fatal("no server Deployment")
	}

	if got := server.raw["spec"].(map[string]any)["replicas"]; got != 1 { //nolint:forcetypeassert // a Deployment has a spec
		t.Errorf("replicas = %v", got)
	}

	c := server.Spec.Template.Spec.Containers[0]
	if strings.Join(c.Args, " ") != "server start --worker" {
		t.Errorf("args = %v", c.Args)
	}

	if !releaseImage.MatchString(c.Image) {
		t.Errorf("image = %q, want a tag the release publishes (v<appVersion>)", c.Image)
	}

	for name, want := range map[string]string{
		"RELIC_DATABASE_URL":        "sqlite:///var/lib/relic/data/relic.db",
		"RELIC_OBJECT_STORAGE_TYPE": "filesystem",
		"RELIC_OBJECT_STORAGE_PATH": "/var/lib/relic/data/objects",
	} {
		if v, _ := c.env(name); v != want {
			t.Errorf("%s = %q, want %q", name, v, want)
		}
	}

	if _, ok := c.env("RELIC_OBJECT_STORAGE_ENDPOINT"); ok {
		t.Error("RELIC_OBJECT_STORAGE_ENDPOINT is set for the filesystem store")
	}

	mounts := map[string]string{}
	for _, m := range c.VolumeMounts {
		mounts[m.Name] = m.MountPath
	}

	if mounts["data"] != "/var/lib/relic/data" || mounts["vulnerability-database"] != "/var/lib/relic/grype" {
		t.Errorf("mounts = %v", mounts)
	}

	sec := find(objs, "Secret", "r-relic-credentials")
	if sec == nil || sec.StringData["databaseURL"] != "" || sec.StringData["objectStorageAccessKey"] != "" {
		t.Errorf("credentials = %+v, want no database URL or S3 keys", sec)
	}
}

func TestChartExternalStorage(t *testing.T) {
	t.Parallel()

	objs, stderr := render(t, external...)
	if stderr != "" {
		t.Fatal(stderr)
	}

	if n := count(objs, "StatefulSet"); n != 0 {
		t.Errorf("%d StatefulSets with external storage", n)
	}

	server := find(objs, "Deployment", "r-relic-server")
	if server == nil {
		t.Fatal("no server Deployment")
	}

	pod := server.Spec.Template.Spec
	if len(pod.InitContainers) != 0 {
		t.Errorf("init containers %+v", pod.InitContainers)
	}

	if v, _ := pod.Containers[0].env("RELIC_OBJECT_STORAGE_ENDPOINT"); v != "http://s3.storage:8333" {
		t.Errorf("RELIC_OBJECT_STORAGE_ENDPOINT = %q", v)
	}

	sec := find(objs, "Secret", "r-relic-credentials")
	if sec == nil || len(sec.StringData["adminPassword"]) != 24 ||
		sec.StringData["encryptionKey"] != testEncryptionKey {
		t.Errorf("generated admin password and the given encryption key: %+v", sec)
	}

	if sec != nil && sec.Metadata.Annotations["helm.sh/resource-policy"] != "keep" {
		t.Errorf(
			"credentials Secret annotations = %v, want it kept on uninstall with the data",
			sec.Metadata.Annotations,
		)
	}

	if _, stderr := render(t, append(external, "--set", "credentials.encryptionKey=")...); !strings.Contains(
		stderr,
		"credentials.encryptionKey (or existingCredentialsSecret) is required",
	) {
		t.Errorf("rendering without an encryption key: %q, want it refused", stderr)
	}

	if find(objs, "PodDisruptionBudget", "r-relic-server") == nil {
		t.Error("no PodDisruptionBudget")
	}
}

func TestChartDependencies(t *testing.T) {
	t.Parallel()

	objs, stderr := render(t, cnpg...)
	if stderr != "" {
		t.Fatal(stderr)
	}

	if find(objs, "Cluster", "r-postgresql") == nil {
		t.Error("no CloudNativePG Cluster r-postgresql")
	}

	if find(objs, "Deployment", "r-seaweedfs-s3") == nil || find(objs, "StatefulSet", "r-seaweedfs-master") == nil {
		t.Error("no SeaweedFS S3 gateway or master")
	}

	for _, name := range []string{
		"r-seaweedfs-master",
		"r-seaweedfs-volume",
		"r-seaweedfs-filer",
	} {
		set := find(objs, "StatefulSet", name)
		if set == nil {
			t.Errorf("no StatefulSet %s", name)
			continue
		}

		spec := set.raw["spec"].(map[string]any) //nolint:forcetypeassert // a StatefulSet has a spec
		if claims, _ := spec["volumeClaimTemplates"].([]any); len(claims) == 0 {
			t.Errorf("%s keeps its data without a volume claim", name)
		}

		volumes, _ := spec["template"].(map[string]any)["spec"].(map[string]any)["volumes"].([]any)
		for _, v := range volumes {
			if hostPath, ok := v.(map[string]any)["hostPath"]; ok {
				t.Errorf("%s mounts a hostPath, %v, which does not follow a rescheduled pod", name, hostPath)
			}
		}
	}

	sec := find(objs, "Secret", "r-relic-credentials")
	if sec == nil || sec.StringData["databaseURL"] != "" || sec.StringData["objectStorageAccessKey"] != "" ||
		len(sec.StringData["adminPassword"]) != 24 {
		t.Errorf("Relic's Secret = %+v", sec)
	}

	for _, name := range []string{
		"r-relic-server",
		"r-relic-worker",
	} {
		pod := find(objs, "Deployment", name).Spec.Template.Spec

		c := pod.Containers[0]
		for env, want := range map[string]string{ //nolint:gosec // G101: Secret names and keys, not credentials
			"RELIC_DATABASE_URL":              "r-postgresql-app/uri",
			"RELIC_OBJECT_STORAGE_ACCESS_KEY": "r-seaweedfs-s3-secret/admin_access_key_id",
			"RELIC_OBJECT_STORAGE_SECRET_KEY": "r-seaweedfs-s3-secret/admin_secret_access_key",
		} {
			if got := c.secretRef(env); got != want {
				t.Errorf("%s %s = %s, want %s", name, env, got, want)
			}
		}

		if v, _ := c.env("RELIC_OBJECT_STORAGE_ENDPOINT"); v != "http://r-seaweedfs-s3:8333" {
			t.Errorf("%s RELIC_OBJECT_STORAGE_ENDPOINT = %q", name, v)
		}

		if len(pod.InitContainers) != 0 {
			t.Errorf("%s init containers %+v", name, pod.InitContainers)
		}
	}
}

func TestChartWorkerVulnerabilityDatabaseWithoutPersistence(t *testing.T) {
	t.Parallel()

	objs, stderr := render(t, append([]string{
		"--set",
		"worker.vulnerabilityDatabase.persistence=false",
	}, external...)...)
	if stderr != "" {
		t.Fatal(stderr)
	}

	for _, v := range find(objs, "Deployment", "r-relic-worker").Spec.Template.Spec.Volumes {
		if v.Name != "vulnerability-database" {
			continue
		}

		if v.EmptyDir == nil || v.EmptyDir.SizeLimit != "5Gi" {
			t.Errorf("vulnerability-database = %+v, want an emptyDir limited to the volume's 5Gi", v)
		}

		return
	}

	t.Error("the worker has no vulnerability-database volume")
}

func TestChartLDAPCA(t *testing.T) {
	t.Parallel()

	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

	objs, stderr := render(t, append([]string{
		"--set-string",
		"ldap.caCertificate=" + pem,
	}, external...)...)
	if stderr != "" {
		t.Fatal(stderr)
	}

	cm := find(objs, "ConfigMap", "r-relic-ldap-ca")
	if cm == nil || cm.Data["ca.crt"] != pem {
		t.Fatalf("ConfigMap = %+v", cm)
	}

	server := find(objs, "Deployment", "r-relic-server")

	c := server.Spec.Template.Spec.Containers[0]
	if v, _ := c.env("RELIC_LDAP_CA_CERTIFICATE_FILE"); v != "/etc/relic/ldap/ca.crt" {
		t.Errorf("RELIC_LDAP_CA_CERTIFICATE_FILE = %q", v)
	}

	mounted := false
	for _, m := range c.VolumeMounts {
		mounted = mounted || (m.Name == "ldap-ca" && m.MountPath == "/etc/relic/ldap")
	}

	if !mounted {
		t.Errorf("mounts = %+v", c.VolumeMounts)
	}

	if server.Spec.Template.Metadata.Annotations["checksum/ldap-ca"] == "" {
		t.Error("no checksum/ldap-ca annotation: a CA change would not roll the pods")
	}
}

func TestChartExistingSecret(t *testing.T) {
	t.Parallel()

	objs, stderr := render(
		t,
		"--set",
		"existingCredentialsSecret=mine",
		"--set",
		"objectStorage.endpoint=http://s3:8333",
		"--set",
		"postgresql.enabled=false",
		"--set",
		"seaweedfs.enabled=false",
	)
	if stderr != "" {
		t.Fatal(stderr)
	}

	if n := count(objs, "Secret"); n != 0 {
		t.Errorf("%d Secrets rendered with existingCredentialsSecret", n)
	}
}

func TestChartIngressAndAutoscaling(t *testing.T) {
	t.Parallel()

	objs, stderr := render(t, append([]string{
		"--set",
		"ingress.enabled=true",
		"--set",
		"ingress.tls[0].secretName=relic-tls",
		"--set",
		"ingress.tls[0].hosts[0]=relic.example.com",
		"--set",
		"server.autoscaling.enabled=true",
	}, external...)...)
	if stderr != "" {
		t.Fatal(stderr)
	}

	if find(objs, "Ingress", "r-relic") == nil || find(objs, "HorizontalPodAutoscaler", "r-relic-server") == nil {
		t.Errorf("Ingress or HPA missing")
	}
}

func TestChartCloudStorage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
		env    map[string]string
		secret map[string]string
		absent []string
	}{
		{
			name: "AWS S3 with IRSA and SSE-KMS",
			values: []string{
				"--set",
				"objectStorage.endpoint=",
				"--set",
				"objectStorage.defaultCredentials=true",
				"--set",
				"objectStorage.serverSideEncryption=aws:kms",
				"--set",
				"objectStorage.sseKmsKeyId=alias/relic",
				"--set",
				"objectStorage.storageClass=INTELLIGENT_TIERING",
				"--set",
				"serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn=arn:aws:iam::1:role/relic",
			},
			env: map[string]string{
				"RELIC_OBJECT_STORAGE_TYPE":           "s3",
				"RELIC_OBJECT_STORAGE_SSE":            "aws:kms",
				"RELIC_OBJECT_STORAGE_SSE_KMS_KEY_ID": "alias/relic",
				"RELIC_OBJECT_STORAGE_STORAGE_CLASS":  "INTELLIGENT_TIERING",
			},
			absent: []string{
				"RELIC_OBJECT_STORAGE_ACCESS_KEY",
				"RELIC_OBJECT_STORAGE_SECRET_KEY",
			},
		},
		{
			name: "Azure with a connection string",
			values: []string{
				"--set",
				"objectStorage.type=azure",
				"--set",
				"objectStorage.endpoint=https://relic.blob.core.windows.net",
				"--set",
				"credentials.objectStorageAzureConnectionString=DefaultEndpointsProtocol=https",
				"--set",
				"podLabels.azure\\.workload\\.identity/use=true",
			},
			env: map[string]string{
				"RELIC_OBJECT_STORAGE_TYPE":     "azure",
				"RELIC_OBJECT_STORAGE_ENDPOINT": "https://relic.blob.core.windows.net",
			},
			secret: map[string]string{
				"RELIC_OBJECT_STORAGE_AZURE_CONNECTION_STRING": "r-relic-credentials/objectStorageAzureConnectionString",
			},
			absent: []string{
				"RELIC_OBJECT_STORAGE_ACCESS_KEY",
			},
		},
		{
			name: "Google Cloud Storage with workload identity",
			values: []string{
				"--set",
				"objectStorage.type=gcs",
				"--set",
				"objectStorage.endpoint=",
				"--set",
				"objectStorage.defaultCredentials=true",
			},
			env: map[string]string{
				"RELIC_OBJECT_STORAGE_TYPE": "gcs",
			},
			absent: []string{
				"RELIC_OBJECT_STORAGE_ACCESS_KEY",
				"RELIC_OBJECT_STORAGE_AZURE_CONNECTION_STRING",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objs, stderr := render(t, append(slices.Clone(external), tt.values...)...)
			if stderr != "" {
				t.Fatal(stderr)
			}

			server := find(objs, "Deployment", "r-relic-server")
			if server == nil {
				t.Fatal("no server Deployment")
			}

			c := server.Spec.Template.Spec.Containers[0]
			for name, want := range tt.env {
				if got, _ := c.env(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}

			for name, want := range tt.secret {
				if got := c.secretRef(name); got != want {
					t.Errorf("%s from %q, want %q", name, got, want)
				}
			}

			for _, name := range tt.absent {
				if _, ok := c.env(name); ok {
					t.Errorf("%s is set", name)
				}
			}
		})
	}

	objs, _ := render(t, append(
		slices.Clone(external),
		"--set",
		"serviceAccount.annotations.iam\\.gke\\.io/gcp-service-account=relic@p.iam.gserviceaccount.com",
	)...)
	if sa := find(objs, "ServiceAccount", "r-relic"); sa == nil ||
		sa.Metadata.Annotations["iam.gke.io/gcp-service-account"] != "relic@p.iam.gserviceaccount.com" {
		t.Errorf("service account = %+v", sa)
	}
}

func TestChartServerLeavesTheServiceBeforeStopping(t *testing.T) {
	t.Parallel()

	objs, stderr := render(t, external...)
	if stderr != "" {
		t.Fatal(stderr)
	}

	server := find(objs, "Deployment", "r-relic-server")
	if server == nil {
		t.Fatal("no server Deployment")
	}

	if got := server.Spec.Template.Spec.Containers[0].Lifecycle.PreStop.Sleep.Seconds; got < 5 {
		t.Errorf("preStop sleep = %ds, want at least 5s for the Service to drop the pod before it stops accepting", got)
	}
}

func TestChartTLS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		args            []string
		wantScheme      string
		wantServicePort int
		wantNoTLS       bool
		wantEnv         map[string]string
		wantMount       string
		wantVolume      string
	}{
		{
			name:            "a certificate authority each pod generates",
			args:            external,
			wantScheme:      "HTTPS",
			wantServicePort: 443,
			wantEnv: map[string]string{
				"RELIC_TLS_MIN_VERSION": "1.2",
				"RELIC_TLS_DIRECTORY":   "/etc/relic/tls",
				"RELIC_TLS_HOSTNAMES":   "r-relic,r-relic.default,r-relic.default.svc,r-relic.default.svc.cluster.local,relic.example.com",
			},
			wantMount:  "/etc/relic/tls",
			wantVolume: "tls",
		},
		{
			name: "the operator's certificate",
			args: append([]string{
				"--set",
				"tls.existingSecret=relic-certificate",
			}, external...),
			wantScheme:      "HTTPS",
			wantServicePort: 443,
			wantEnv: map[string]string{
				"RELIC_TLS_CERTIFICATE_FILE": "/etc/relic/tls/tls.crt",
				"RELIC_TLS_KEY_FILE":         "/etc/relic/tls/tls.key",
			},
			wantMount:  "/etc/relic/tls",
			wantVolume: "tls",
		},
		{
			name:            "a single node keeps its authority on the data volume",
			args:            singleNode,
			wantScheme:      "HTTPS",
			wantServicePort: 443,
			wantEnv: map[string]string{
				"RELIC_TLS_DIRECTORY": "/var/lib/relic/data/tls",
			},
		},
		{
			name: "TLS 1.3 only",
			args: append([]string{
				"--set-string",
				"tls.minVersion=1.3",
			}, external...),
			wantScheme:      "HTTPS",
			wantServicePort: 443,
			wantEnv: map[string]string{
				"RELIC_TLS_MIN_VERSION": "1.3",
			},
		},
		{
			name: "plain HTTP behind a proxy that terminates TLS",
			args: append([]string{
				"--set",
				"tls.enabled=false",
			}, external...),
			wantScheme:      "HTTP",
			wantServicePort: 80,
			wantNoTLS:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objs, stderr := render(t, append([]string{
				"--set",
				"ingress.enabled=true",
			}, tt.args...)...)
			if stderr != "" {
				t.Fatal(stderr)
			}

			server := find(objs, "Deployment", "r-relic-server")
			c := server.Spec.Template.Spec.Containers[0]

			if got := slices.Contains(c.Args, "--no-tls"); got != tt.wantNoTLS {
				t.Errorf("args = %v", c.Args)
			}

			for _, probe := range []string{
				"startupProbe",
				"readinessProbe",
				"livenessProbe",
			} {
				if got := dig(
					server.raw,
					"spec",
					"template",
					"spec",
					"containers",
					0,
					probe,
					"httpGet",
					"scheme",
				); got != tt.wantScheme {
					t.Errorf("%s scheme = %v, want %s", probe, got, tt.wantScheme)
				}
			}

			for name, want := range tt.wantEnv {
				if got, _ := c.env(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}

			if tt.wantNoTLS {
				for _, name := range []string{
					"RELIC_TLS_DIRECTORY",
					"RELIC_TLS_CERTIFICATE_FILE",
					"RELIC_TLS_MIN_VERSION",
				} {
					if _, ok := c.env(name); ok {
						t.Errorf("%s is set without TLS", name)
					}
				}
			}

			if tt.wantMount != "" && !slices.ContainsFunc(c.VolumeMounts, func(m struct {
				Name      string `yaml:"name"`
				MountPath string `yaml:"mountPath"`
			},
			) bool {
				return m.Name == tt.wantVolume && m.MountPath == tt.wantMount
			}) {
				t.Errorf("mounts = %+v, want %s at %s", c.VolumeMounts, tt.wantVolume, tt.wantMount)
			}

			service := find(objs, "Service", "r-relic")
			if got := dig(service.raw, "spec", "ports", 0, "port"); got != tt.wantServicePort {
				t.Errorf("Service port = %v, want %d", got, tt.wantServicePort)
			}

			ingress := find(objs, "Ingress", "r-relic")

			backendProtocol := ingress.Metadata.Annotations["nginx.ingress.kubernetes.io/backend-protocol"]
			if (backendProtocol == "HTTPS") != (tt.wantScheme == "HTTPS") {
				t.Errorf("Ingress backend protocol annotation = %q", backendProtocol)
			}
		})
	}
}
