package main

import (
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
