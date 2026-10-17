package helm_test

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func dig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}

			v = m[k]
		case int:
			l, ok := v.([]any)
			if !ok || k >= len(l) {
				return nil
			}

			v = l[k]
		}
	}

	return v
}

func mustRender(t *testing.T, args ...string) []object {
	t.Helper()

	objs, stderr := render(t, args...)
	if stderr != "" {
		t.Fatal(stderr)
	}

	return objs
}

func relicContainers(t *testing.T, objs []object) map[string]container {
	t.Helper()

	out := map[string]container{}

	for _, name := range []string{
		"r-relic-server",
		"r-relic-worker",
	} {
		d := find(objs, "Deployment", name)
		if d == nil {
			t.Fatalf("no Deployment %s", name)
		}

		out[name] = d.Spec.Template.Spec.Containers[0]
	}

	return out
}

func hasPort(c container, name string, port int) bool {
	for _, p := range c.Ports {
		if p.Name == name && p.ContainerPort == port {
			return true
		}
	}

	return false
}

func TestChartBundledOperator(t *testing.T) {
	t.Parallel()
	objs := mustRender(t)

	op := find(objs, "Deployment", "r-cloudnative-pg")
	if op == nil || find(objs, "Cluster", "r-postgresql") == nil {
		t.Fatal("no operator Deployment r-cloudnative-pg or Cluster r-postgresql")
	}

	if got := dig(op.raw, "metadata", "labels", "app.kubernetes.io/name"); got != "cloudnative-pg" {
		t.Errorf("operator label app.kubernetes.io/name = %v, want cloudnative-pg", got)
	}

	if n := count(objs, "CustomResourceDefinition"); n != 0 {
		t.Errorf("%d CRDs rendered from templates; they belong in crds/", n)
	}

	for _, kind := range []string{
		"MutatingWebhookConfiguration",
		"ValidatingWebhookConfiguration",
	} {
		for _, o := range objs {
			if o.Kind != kind {
				continue
			}

			for _, w := range dig(o.raw, "webhooks").([]any) {
				if p := dig(w, "failurePolicy"); p != "Ignore" {
					t.Errorf("%s %s: failurePolicy %v", kind, o.Metadata.Name, p)
				}
			}
		}
	}

	crds, err := os.ReadFile(chart + "/crds/cloudnative-pg.yaml")
	if err != nil || !strings.Contains(string(crds), "name: clusters.postgresql.cnpg.io") ||
		strings.Contains(string(crds), "{{") {
		t.Errorf("crds/cloudnative-pg.yaml: %v; it must hold the Cluster CRD, untemplated", err)
	}

	objs = mustRender(t, append([]string{
		"--set",
		"postgresql-operator.enabled=false",
	}, cnpg...)...)
	if find(objs, "Deployment", "r-cloudnative-pg") != nil || find(objs, "Cluster", "r-postgresql") == nil {
		t.Error("postgresql-operator.enabled=false: operator rendered, or no Cluster")
	}
}

func TestChartMetrics(t *testing.T) {
	t.Parallel()
	t.Run("defaults: metrics port, headless Service, NetworkPolicy, no monitors without the API", func(t *testing.T) {
		t.Parallel()

		objs := mustRender(t, external...)
		for name, c := range relicContainers(t, objs) {
			if v, _ := c.env("RELIC_METRICS_LISTEN_ADDRESS"); v != ":8091" || !hasPort(c, "metrics", 8091) {
				t.Errorf("%s: RELIC_METRICS_LISTEN_ADDRESS %q, ports %+v", name, v, c.Ports)
			}

			if _, ok := c.env("OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
				t.Errorf("%s: tracing on without an endpoint", name)
			}
		}

		svc := find(objs, "Service", "r-relic-metrics")
		if svc == nil || dig(svc.raw, "spec", "clusterIP") != "None" ||
			dig(svc.raw, "spec", "ports", 0, "port") != 8091 {
			t.Errorf("metrics Service = %+v", svc)
		}

		if main := find(objs, "Service", "r-relic"); main == nil || len(dig(main.raw, "spec", "ports").([]any)) != 1 {
			t.Error("the main Service must carry only the http port")
		}

		for _, np := range []string{
			"r-relic-server",
			"r-relic-worker",
		} {
			o := find(objs, "NetworkPolicy", np)
			if o == nil || !strings.Contains(toString(dig(o.raw, "spec", "ingress")), "port: metrics") {
				t.Errorf("NetworkPolicy %s does not admit the metrics port: %v", np, dig(o.raw, "spec", "ingress"))
			}
		}

		if count(objs, "ServiceMonitor")+count(objs, "PrometheusRule") != 0 {
			t.Error("monitors rendered without the monitoring.coreos.com/v1 API")
		}
	})
	t.Run("with the Prometheus Operator: ServiceMonitor and rules", func(t *testing.T) {
		t.Parallel()
		objs := mustRender(
			t,
			append(
				append(
					[]string{
						"--set",
						"metrics.prometheusRule.runbookBaseURL=https://relic.example.com/",
					},
					external...),
				promOperator...)...)

		sm := find(objs, "ServiceMonitor", "r-relic")
		if sm == nil || dig(sm.raw, "spec", "endpoints", 0, "port") != "metrics" ||
			sm.Metadata.Labels["release"] != "r" {
			t.Errorf("ServiceMonitor = %+v", sm)
		}

		rule := find(objs, "PrometheusRule", "r-relic")
		if rule == nil {
			t.Fatal("no PrometheusRule r-relic")
		}

		groups := dig(rule.raw, "spec", "groups").([]any)
		if len(groups) != 2 ||
			dig(
				groups[1],
				"rules",
				0,
				"annotations",
				"runbook_url",
			) != "https://relic.example.com/docs/guides/monitoring#relicdown" {
			t.Errorf(
				"rules: %d groups, first alert's runbook %v",
				len(groups),
				dig(groups[1], "rules", 0, "annotations", "runbook_url"),
			)
		}
	})
	t.Run("monitors switched off", func(t *testing.T) {
		t.Parallel()

		objs := mustRender(
			t,
			append(
				append(
					[]string{
						"--set",
						"metrics.serviceMonitor.enabled=false",
						"--set",
						"metrics.prometheusRule.enabled=false",
					},
					external...),
				promOperator...)...)
		if find(objs, "ServiceMonitor", "r-relic") != nil || find(objs, "PrometheusRule", "r-relic") != nil {
			t.Error("monitors rendered while switched off")
		}
	})
	t.Run("metrics off", func(t *testing.T) {
		t.Parallel()

		objs := mustRender(
			t,
			append([]string{
				"--set",
				"metrics.enabled=false",
			}, append(external, promOperator...)...)...)
		for name, c := range relicContainers(t, objs) {
			if v, _ := c.env("RELIC_METRICS_LISTEN_ADDRESS"); v != "off" || hasPort(c, "metrics", 8091) {
				t.Errorf("%s: RELIC_METRICS_LISTEN_ADDRESS %q, ports %+v", name, v, c.Ports)
			}
		}

		if find(objs, "Service", "r-relic-metrics") != nil || find(objs, "ServiceMonitor", "r-relic") != nil {
			t.Error("metrics Service or ServiceMonitor with metrics off")
		}
	})
}

func toString(v any) string {
	b, _ := yaml.Marshal(v)
	return string(b)
}

func TestChartTracingAndLogging(t *testing.T) {
	t.Parallel()

	objs := mustRender(t, append([]string{
		"--set",
		"tracing.otlpEndpoint=http://collector.observability:4318",
		"--set",
		"logging.otlp=true",
		"--set",
		"logging.level=debug",
		"--set",
		"logging.format=text",
		"--set",
		"metrics.repositoryNameLabel=true",
		"--set",
		"metrics.profiling=true",
	}, external...)...)
	for name, c := range relicContainers(t, objs) {
		for env, want := range map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT":    "http://collector.observability:4318",
			"OTEL_TRACES_SAMPLER":            "parentbased_traceidratio",
			"OTEL_TRACES_SAMPLER_ARG":        "0.1",
			"OTEL_LOGS_EXPORTER":             "otlp",
			"RELIC_LOG_LEVEL":                "debug",
			"RELIC_LOG_FORMAT":               "text",
			"RELIC_METRICS_REPOSITORY_LABEL": "true",
			"RELIC_PROFILING_ENABLED":        "true",
		} {
			if got, _ := c.env(env); got != want {
				t.Errorf("%s %s = %q, want %q", name, env, got, want)
			}
		}

		if got, _ := c.env("OTEL_RESOURCE_ATTRIBUTES"); !strings.Contains(got, "k8s.pod.name=$(K8S_POD_NAME)") {
			t.Errorf("%s OTEL_RESOURCE_ATTRIBUTES = %q", name, got)
		}
	}
}

func TestChartDashboards(t *testing.T) {
	t.Parallel()

	for _, o := range mustRender(t, external...) {
		if strings.HasPrefix(o.Metadata.Name, "r-relic-dashboard-") {
			t.Errorf("dashboard %s rendered by default", o.Metadata.Name)
		}
	}

	objs := mustRender(
		t,
		append(
			[]string{
				"--set",
				"grafana.dashboards.enabled=true",
				"--set",
				"grafana.dashboards.folder=Artifacts",
			},
			external...)...)
	n := 0

	for _, o := range objs {
		if o.Kind != "ConfigMap" || !strings.HasPrefix(o.Metadata.Name, "r-relic-dashboard-") {
			continue
		}

		n++

		if o.Metadata.Labels["grafana_dashboard"] != "1" || o.Metadata.Annotations["grafana_folder"] != "Artifacts" ||
			len(o.Data) != 1 {
			t.Errorf(
				"%s: labels %v, annotations %v, %d keys",
				o.Metadata.Name,
				o.Metadata.Labels,
				o.Metadata.Annotations,
				len(o.Data),
			)
		}
	}

	if n != 8 {
		t.Errorf("%d dashboard ConfigMaps, want Relic's seven and CloudNativePG's", n)
	}
}

func TestChartTelemetry(t *testing.T) {
	t.Parallel()

	objs := mustRender(t, append([]string{
		"--set",
		"monitoringStack.enabled=true",
	}, promOperator...)...)
	for name, c := range relicContainers(t, objs) {
		if v, _ := c.env("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "http://r-alloy:4318" {
			t.Errorf("%s OTEL_EXPORTER_OTLP_ENDPOINT = %q, want the bundled Alloy", name, v)
		}
	}

	for _, want := range []struct {
		kind, name string
	}{
		{
			"Deployment",
			"r-grafana",
		},
		{
			"Deployment",
			"r-kube-prometheus-stack-operator",
		},
		{
			"Prometheus",
			"r-kube-prometheus-stack-prometheus",
		},
		{
			"StatefulSet",
			"r-tempo",
		},
		{
			"StatefulSet",
			"r-loki",
		},
		{
			"Deployment",
			"r-alloy",
		},
		{
			"ServiceMonitor",
			"r-relic",
		},
		{
			"PrometheusRule",
			"r-relic",
		},
		{
			"ConfigMap",
			"r-relic-dashboard-overview",
		},
	} {
		if find(objs, want.kind, want.name) == nil {
			t.Errorf("no %s %s", want.kind, want.name)
		}
	}

	ds := find(objs, "ConfigMap", "r-relic-datasources")
	if ds == nil || ds.Metadata.Labels["grafana_datasource"] != "1" ||
		!strings.Contains(
			ds.Data["relic-datasources.yaml"],
			"url: http://r-tempo:3200",
		) || !strings.Contains(ds.Data["relic-datasources.yaml"], "url: http://r-loki:3100") {
		t.Errorf("datasources = %+v", ds)
	}

	alloy := find(objs, "ConfigMap", "r-alloy")
	if alloy == nil || !strings.Contains(alloy.Data["config.alloy"], `"http://r-loki:3100/loki/api/v1/push"`) ||
		!strings.Contains(alloy.Data["config.alloy"], `"r-tempo:4317"`) {
		t.Errorf("Alloy's configuration does not reach this release's Loki and Tempo: %+v", alloy)
	}

	prom := find(objs, "Prometheus", "r-kube-prometheus-stack-prometheus")
	if features := toString(dig(prom.raw, "spec", "enableFeatures")); !strings.Contains(features, "exemplar-storage") {
		t.Errorf("Prometheus features %q: exemplars off", features)
	}
}
