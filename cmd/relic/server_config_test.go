package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaskedSettingsLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "password setting",
			line: "RELIC_ADMIN_PASSWORD=hunter2\n",
			want: "RELIC_ADMIN_PASSWORD=********\n",
		},
		{
			name: "plain setting",
			line: "RELIC_LOG_LEVEL=debug\n",
			want: "RELIC_LOG_LEVEL=debug\n",
		},
		{
			name: "comment",
			line: "# RELIC_ADMIN_PASSWORD=hunter2\n",
			want: "# RELIC_ADMIN_PASSWORD=hunter2\n",
		},
		{
			name: "password in a database URL",
			line: "RELIC_DATABASE_URL=postgres://relic:Sup3rSecretDbPw@db:5432/relic?sslmode=require\n",
			want: "RELIC_DATABASE_URL=postgres://relic:********@db:5432/relic?sslmode=require\n",
		},
		{
			name: "password in a URL's query",
			line: "RELIC_DATABASE_URL=postgres://db:5432/relic?user=relic&password=Sup3rSecretDbPw\n",
			want: "RELIC_DATABASE_URL=postgres://db:5432/relic?user=relic&password=********\n",
		},
		{
			name: "password in a keyword/value connection string",
			line: "RELIC_DATABASE_URL=host=db user=relic password=Sup3rSecretDbPw dbname=relic\n",
			want: "RELIC_DATABASE_URL=host=db user=relic password=******** dbname=relic\n",
		},
		{
			name: "quoted secrets in a keyword/value connection string",
			line: "RELIC_DATABASE_URL=host=db password = 'Sup3r Secret\\' DbPw' sslpassword='KeyPw' dbname=relic\n",
			want: "RELIC_DATABASE_URL=host=db password = ******** sslpassword=******** dbname=relic\n",
		},
		{
			name: "keyword/value connection string without secrets",
			line: "RELIC_DATABASE_URL=host=db user=relic dbname=relic sslmode=require\n",
			want: "RELIC_DATABASE_URL=host=db user=relic dbname=relic sslmode=require\n",
		},
		{
			name: "URL without credentials",
			line: "RELIC_DATABASE_URL=sqlite:///var/lib/relic/relic.db\n",
			want: "RELIC_DATABASE_URL=sqlite:///var/lib/relic/relic.db\n",
		},
		{
			name: "Azure connection string",
			line: "RELIC_OBJECT_STORAGE_AZURE_CONNECTION_STRING=DefaultEndpointsProtocol=https;" +
				"AccountName=acct;AccountKey=AzureAccountKeyValue==\n",
			want: "RELIC_OBJECT_STORAGE_AZURE_CONNECTION_STRING=********\n",
		},
		{
			name: "OTLP headers",
			line: "OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer otlpTokenValue\n",
			want: "OTEL_EXPORTER_OTLP_HEADERS=********\n",
		},
		{
			name: "OTLP trace headers",
			line: "OTEL_EXPORTER_OTLP_TRACES_HEADERS=api-key=otlpTokenValue\n",
			want: "OTEL_EXPORTER_OTLP_TRACES_HEADERS=********\n",
		},
		//nolint:gosec // G101: a made-up credential the test masks
		{
			name: "credentials in an endpoint",
			line: "OTEL_EXPORTER_OTLP_ENDPOINT=https://user:otlpTokenValue@collector.example.com:4318\n",
			want: "OTEL_EXPORTER_OTLP_ENDPOINT=https://user:********@collector.example.com:4318\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := maskedSettingsLine(tt.line); got != tt.want {
				t.Errorf("maskedSettingsLine(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

func TestWithSetting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		replace bool
		want    string
	}{
		{
			name:    "missing",
			content: "RELIC_LOG_FORMAT=text",
			replace: true,
			want:    "RELIC_LOG_FORMAT=text\nRELIC_LOG_LEVEL=debug\n",
		},
		{
			name:    "set",
			content: "RELIC_LOG_LEVEL=info\nRELIC_LOG_FORMAT=text\n",
			replace: true,
			want:    "RELIC_LOG_LEVEL=debug\nRELIC_LOG_FORMAT=text\n",
		},
		{
			name:    "set twice, of which the last applies",
			content: "RELIC_LOG_LEVEL=info\nRELIC_LOG_FORMAT=text\nRELIC_LOG_LEVEL=warn\n",
			replace: true,
			want:    "RELIC_LOG_LEVEL=debug\nRELIC_LOG_FORMAT=text\nRELIC_LOG_LEVEL=debug\n",
		},
		{
			name:    "indented",
			content: "  RELIC_LOG_LEVEL=info\n",
			replace: true,
			want:    "RELIC_LOG_LEVEL=debug\n",
		},
		{
			name:    "kept",
			content: "RELIC_LOG_LEVEL=info\n",
			want:    "RELIC_LOG_LEVEL=info\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := withSetting(tt.content, "RELIC_LOG_LEVEL", "debug", tt.replace); got != tt.want {
				t.Errorf("withSetting = %q, want %q", got, tt.want)
			}

			if got := keyValues(withSetting(tt.content, "RELIC_LOG_LEVEL", "debug", true), "=")["RELIC_LOG_LEVEL"]; got != "debug" {
				t.Errorf("the setting that applies is %q, want debug", got)
			}
		})
	}
}

func TestServerConfigSetChangesSeveralSettingsWithOneRestart(t *testing.T) {
	t.Parallel()

	m, ran, _ := testMachineSetup(t, "")
	m.yes = true

	if err := m.runSetup(t.Context()); err != nil {
		t.Fatal(err)
	}

	*ran = nil
	path := filepath.Join(m.configDir(), "server.env")

	if err := m.setServiceSettings(t.Context(), &bytes.Buffer{}, path, setting{
		key:   "RELIC_SCAN_SYFT_PATH",
		value: "/opt/bin/syft",
	}, setting{
		key:   "RELIC_SCAN_GRYPE_PATH",
		value: "/opt/bin/grype",
	}); err != nil {
		t.Fatal(err)
	}

	settings := readFile(t, path)
	if !strings.Contains(settings, "RELIC_SCAN_SYFT_PATH=/opt/bin/syft\n") ||
		!strings.Contains(settings, "RELIC_SCAN_GRYPE_PATH=/opt/bin/grype\n") {
		t.Errorf("settings:\n%s", settings)
	}

	want := []string{
		"systemctl --user reset-failed relic.service",
		"systemctl --user restart relic.service",
	}
	if strings.Join(*ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(*ran, "\n"), strings.Join(want, "\n"))
	}
}

func TestServerConfigSetArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "two keys with --value-stdin",
			args: []string{
				"RELIC_LOG_LEVEL",
				"RELIC_TRASH_ENABLED",
				"--value-stdin",
			},
			want: "give one key",
		},
		{
			name: "the first administrator's password",
			args: []string{
				"RELIC_LOG_LEVEL=debug",
				"RELIC_ADMIN_PASSWORD=Another-pass-123",
			},
			want: "first administrator",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := runRelic(t, map[string]string{
				"HOME": t.TempDir(),
			}, append([]string{
				"server",
				"config",
				"set",
			}, tt.args...)...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("relic server config set %s = %v, want %q", strings.Join(tt.args, " "), err, tt.want)
			}
		})
	}
}
