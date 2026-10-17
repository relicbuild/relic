package config

import (
	"crypto/tls"
	"maps"
	"strings"
	"testing"
	"time"
)

var minimal = map[string]string{
	"RELIC_DATABASE_URL":              "postgres://relic@localhost/relic",
	"RELIC_OBJECT_STORAGE_ENDPOINT":   "http://localhost:8333",
	"RELIC_OBJECT_STORAGE_ACCESS_KEY": "ak",
	"RELIC_OBJECT_STORAGE_SECRET_KEY": "sk",
}

func with(overrides map[string]string) map[string]string {
	env := maps.Clone(minimal)

	for k, v := range overrides {
		if v == "" {
			delete(env, k)
		} else {
			env[k] = v
		}
	}

	return env
}

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		check   func(t *testing.T, c Config)
		wantErr string
	}{
		{
			name: "GC tunables",
			env: with(
				map[string]string{
					"RELIC_GARBAGE_COLLECTION_INTERVAL":     "30s",
					"RELIC_GARBAGE_COLLECTION_GRACE_PERIOD": "2m",
				},
			),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.GarbageCollection.Interval != 30*time.Second || c.GarbageCollection.GracePeriod != 2*time.Minute {
					t.Errorf("GC = %+v", c.GarbageCollection)
				}
			},
		},
		{
			name: "bad GC duration",
			env: with(map[string]string{
				"RELIC_GARBAGE_COLLECTION_GRACE_PERIOD": "-1h",
			}),
			wantErr: "RELIC_GARBAGE_COLLECTION_GRACE_PERIOD",
		},
		{
			name: "AWS S3 with the default credential chain",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_ENDPOINT":       "",
				"RELIC_OBJECT_STORAGE_ACCESS_KEY":     "",
				"RELIC_OBJECT_STORAGE_SECRET_KEY":     "",
				"RELIC_OBJECT_STORAGE_SSE":            "aws:kms",
				"RELIC_OBJECT_STORAGE_SSE_KMS_KEY_ID": "alias/relic",
				"RELIC_OBJECT_STORAGE_STORAGE_CLASS":  "INTELLIGENT_TIERING",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				o := c.ObjectStorage
				if o.Endpoint != "" || o.ServerSideEncryption != "aws:kms" || o.SSEKMSKeyID != "alias/relic" ||
					o.StorageClass != "INTELLIGENT_TIERING" {
					t.Errorf("object storage = %+v", o)
				}
			},
		},
		{
			name: "half a static key",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_SECRET_KEY": "",
			}),
			wantErr: "RELIC_OBJECT_STORAGE_ACCESS_KEY",
		},
		{
			name: "unknown server-side encryption",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_SSE": "rot13",
			}),
			wantErr: "RELIC_OBJECT_STORAGE_SSE",
		},
		{
			name: "an unknown secret provider",
			env: with(map[string]string{
				"RELIC_SECRET_VALIDATION_PROVIDERS": "github,aws",
			}),
			wantErr: "RELIC_SECRET_VALIDATION_PROVIDERS",
		},
		{
			name: "a KMS key without aws:kms",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_SSE_KMS_KEY_ID": "alias/relic",
			}),
			wantErr: "RELIC_OBJECT_STORAGE_SSE_KMS_KEY_ID",
		},
		{
			name: "azure needs an account",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_TYPE":     "azure",
				"RELIC_OBJECT_STORAGE_ENDPOINT": "",
			}),
			wantErr: "RELIC_OBJECT_STORAGE_AZURE_CONNECTION_STRING",
		},
		{
			name: "google cloud storage with application default credentials",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_TYPE":       "gcs",
				"RELIC_OBJECT_STORAGE_ENDPOINT":   "",
				"RELIC_OBJECT_STORAGE_ACCESS_KEY": "",
				"RELIC_OBJECT_STORAGE_SECRET_KEY": "",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.ObjectStorage.Type != "gcs" {
					t.Errorf("type = %q", c.ObjectStorage.Type)
				}
			},
		},
		{
			name: "GC defaults",
			env:  minimal,
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.GarbageCollection.Interval != 10*time.Minute || c.GarbageCollection.GracePeriod != time.Hour {
					t.Errorf("GC = %+v", c.GarbageCollection)
				}
			},
		},
		{
			name: "defaults",
			env:  minimal,
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.HTTPListenAddress != ":8090" || c.ObjectStorage.Region != "us-east-1" ||
					c.ObjectStorage.ArtifactsBucket != "artifacts" ||
					c.ObjectStorage.TemporaryBucket != "temporary" ||
					c.ObjectStorage.Checksum != "when_supported" ||
					c.AdminUsername != "admin" {
					t.Errorf("unexpected defaults: %+v", c)
				}
			},
		},
		{
			name: "vulnerability database: Grype's own by default",
			env:  minimal,
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.Scan.VulnerabilityDatabaseURL != "" || c.Scan.VulnerabilityDatabaseOff {
					t.Errorf("scan = %+v", c.Scan)
				}
			},
		},
		{
			name: "vulnerability database: a mirror",
			env: with(map[string]string{
				"RELIC_SCAN_VULNERABILITY_DATABASE_URL": "https://relic.example/generic/grype/latest.json",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.Scan.VulnerabilityDatabaseURL != "https://relic.example/generic/grype/latest.json" ||
					c.Scan.VulnerabilityDatabaseOff {
					t.Errorf("scan = %+v", c.Scan)
				}
			},
		},
		{
			name: "malicious packages: OSV by default, off",
			env: with(map[string]string{
				"RELIC_MALICIOUS_PACKAGES_URL": "off",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.Scan.MaliciousPackagesURL != "" {
					t.Errorf("scan = %+v", c.Scan)
				}
			},
		},
		{
			name: "end of life: endoflife.date by default; deps.dev off unless set",
			env:  with(map[string]string{}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.Scan.EndOfLifeURL != "https://endoflife.date/api/v1/products/full" || c.Scan.DepsDevURL != "" {
					t.Errorf("scan = %+v", c.Scan)
				}
			},
		},
		{
			name: "end of life off, deps.dev on",
			env: with(map[string]string{
				"RELIC_END_OF_LIFE_URL": "off",
				"RELIC_DEPS_DEV_URL":    "https://api.deps.dev",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.Scan.EndOfLifeURL != "" || c.Scan.DepsDevURL != "https://api.deps.dev" {
					t.Errorf("scan = %+v", c.Scan)
				}
			},
		},
		{
			name: "secret validation: off by default",
			env:  with(map[string]string{}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.Scan.SecretValidation || len(c.Scan.SecretValidationProviders) != 0 {
					t.Errorf("scan = %+v", c.Scan)
				}
			},
		},
		{
			name: "secret validation on, narrowed to two providers",
			//nolint:gosec // setting names, not credentials
			env: with(map[string]string{
				"RELIC_SECRET_VALIDATION_ENABLED":   "true",
				"RELIC_SECRET_VALIDATION_PROVIDERS": "github, npm",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if !c.Scan.SecretValidation || strings.Join(c.Scan.SecretValidationProviders, ",") != "github,npm" {
					t.Errorf("scan = %+v", c.Scan)
				}
			},
		},
		{
			name: "deep license scanning: off unless set",
			env: with(map[string]string{
				"RELIC_SCAN_SCANCODE_PATH": "/opt/scancode/scancode",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.Scan.ScanCodePath != "/opt/scancode/scancode" || c.Scan.GitleaksPath != "gitleaks" {
					t.Errorf("scan = %+v", c.Scan)
				}
			},
		},
		{
			name: "vulnerability database: off",
			env: with(map[string]string{
				"RELIC_SCAN_VULNERABILITY_DATABASE_URL": "off",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.Scan.VulnerabilityDatabaseURL != "" || !c.Scan.VulnerabilityDatabaseOff {
					t.Errorf("scan = %+v", c.Scan)
				}
			},
		},
		{
			name: "overrides",
			env: with(map[string]string{
				"RELIC_HTTP_LISTEN_ADDRESS": "127.0.0.1:9000",
				"RELIC_ADMIN_PASSWORD":      "pw",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.HTTPListenAddress != "127.0.0.1:9000" || c.AdminPassword != "pw" {
					t.Errorf("overrides not applied: %+v", c)
				}
			},
		},
		{
			name: "secrets key",
			env: with(
				map[string]string{
					"RELIC_ENCRYPTION_KEY": "cmVsaWMtZGV2LXNlY3JldHMta2V5LW5vdC1wcm9kISE=",
				},
			), //nolint:gosec // G101: a test key
			check: func(t *testing.T, c Config) {
				t.Helper()

				if string(c.EncryptionKey) != "relic-dev-secrets-key-not-prod!!" {
					t.Errorf("EncryptionKey = %q", c.EncryptionKey)
				}
			},
		},
		{
			name: "short secrets key",
			env: with(map[string]string{
				"RELIC_ENCRYPTION_KEY": "c2hvcnQ=",
			}),
			wantErr: "RELIC_ENCRYPTION_KEY",
		}, //nolint:gosec // G101: a test key
		{
			name: "trusted proxies",
			env: with(map[string]string{
				"RELIC_TRUSTED_PROXIES": "10.0.0.0/8, 127.0.0.1",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if len(c.TrustedProxies) != 2 || c.TrustedProxies[1].String() != "127.0.0.1/32" {
					t.Errorf("TrustedProxies = %v", c.TrustedProxies)
				}
			},
		},
		{
			name: "TLS: a directory in the config home by default",
			env: with(map[string]string{
				"HOME": "/home/relic",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.TLS.Directory != "/home/relic/.config/relic/tls" || c.TLS.CertificateFile != "" {
					t.Errorf("TLS = %+v", c.TLS)
				}
			},
		},
		{
			name: "TLS: XDG_CONFIG_HOME",
			env: with(map[string]string{
				"HOME":            "/home/relic",
				"XDG_CONFIG_HOME": "/srv/config",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.TLS.Directory != "/srv/config/relic/tls" {
					t.Errorf("TLS.Directory = %q", c.TLS.Directory)
				}
			},
		},
		{
			name: "TLS: an operator's certificate, a directory and extra names",
			env: with(map[string]string{
				"RELIC_TLS_CERTIFICATE_FILE": "/etc/relic/tls/tls.crt",
				"RELIC_TLS_KEY_FILE":         "/etc/relic/tls/tls.key",
				"RELIC_TLS_DIRECTORY":        "/var/lib/relic/tls",
				"RELIC_TLS_HOSTNAMES":        "relic.example.com, relic, 10.0.0.5",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.TLS.CertificateFile != "/etc/relic/tls/tls.crt" || c.TLS.KeyFile != "/etc/relic/tls/tls.key" ||
					c.TLS.Directory != "/var/lib/relic/tls" ||
					strings.Join(c.TLS.Hostnames, " ") != "relic.example.com relic 10.0.0.5" {
					t.Errorf("TLS = %+v", c.TLS)
				}
			},
		},
		{
			name: "TLS: 1.2 and up by default",
			env:  minimal,
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.TLS.MinVersion != tls.VersionTLS12 {
					t.Errorf("TLS.MinVersion = %x", c.TLS.MinVersion)
				}
			},
		},
		{
			name: "TLS: 1.3 only",
			env: with(map[string]string{
				"RELIC_TLS_MIN_VERSION": "1.3",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.TLS.MinVersion != tls.VersionTLS13 {
					t.Errorf("TLS.MinVersion = %x", c.TLS.MinVersion)
				}
			},
		},
		{
			name: "TLS: nothing below 1.2",
			env: with(map[string]string{
				"RELIC_TLS_MIN_VERSION": "1.1",
			}),
			wantErr: "RELIC_TLS_MIN_VERSION",
		},
		{
			name: "TLS: a certificate without its key",
			env: with(map[string]string{
				"RELIC_TLS_CERTIFICATE_FILE": "/etc/relic/tls/tls.crt",
			}),
			wantErr: "RELIC_TLS_KEY_FILE",
		},
		{
			name: "TLS: a URL among the names",
			env: with(map[string]string{
				"RELIC_TLS_HOSTNAMES": "relic.example.com, https://relic.example.com",
			}),
			wantErr: "RELIC_TLS_HOSTNAMES",
		},
		{
			name: "TLS: a name with a space",
			env: with(map[string]string{
				"RELIC_TLS_HOSTNAMES": "bad name",
			}),
			wantErr: "RELIC_TLS_HOSTNAMES",
		},
		{
			name: "TLS: a name with a label that starts with a hyphen",
			env: with(map[string]string{
				"RELIC_TLS_HOSTNAMES": "-relic.example.com",
			}),
			wantErr: "RELIC_TLS_HOSTNAMES",
		},
		{
			name: "TLS: wildcard names and IPv6 addresses",
			env: with(map[string]string{
				"RELIC_TLS_HOSTNAMES": "*.relic.example.com,fd00::5,relic-1.internal",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if len(c.TLS.Hostnames) != 3 {
					t.Errorf("hostnames = %v", c.TLS.Hostnames)
				}
			},
		},
		{
			name: "TLS: a relative directory",
			env: with(map[string]string{
				"RELIC_TLS_DIRECTORY": "tls",
			}),
			wantErr: "RELIC_TLS_DIRECTORY",
		},
		{
			name: "bad trusted proxy",
			env: with(map[string]string{
				"RELIC_TRUSTED_PROXIES": "10.0.0.0/99",
			}),
			wantErr: "RELIC_TRUSTED_PROXIES",
		},
		{
			name: "connection and statement limits",
			env: with(map[string]string{
				"RELIC_DATABASE_STATEMENT_TIMEOUT": "30s",
				"RELIC_MAX_CONNECTIONS":            "0",
				"RELIC_DATABASE_MAX_CONNECTIONS":   "64",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.DatabaseStatementTimeout != 30*time.Second || c.MaxConnections != 0 ||
					c.DatabaseMaxConnections != 64 {
					t.Errorf(
						"StatementTimeout, MaxConnections, DatabaseMaxConnections = %v, %d, %d",
						c.DatabaseStatementTimeout,
						c.MaxConnections,
						c.DatabaseMaxConnections,
					)
				}
			},
		},
		{
			name: "limit defaults",
			env:  minimal,
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.DatabaseStatementTimeout != 5*time.Minute || c.MaxConnections != 8192 ||
					c.DatabaseMaxConnections != 20 {
					t.Errorf(
						"StatementTimeout, MaxConnections, DatabaseMaxConnections = %v, %d, %d",
						c.DatabaseStatementTimeout,
						c.MaxConnections,
						c.DatabaseMaxConnections,
					)
				}
			},
		},
		{
			name: "bad database pool size",
			env: with(map[string]string{
				"RELIC_DATABASE_MAX_CONNECTIONS": "0",
			}),
			wantErr: "RELIC_DATABASE_MAX_CONNECTIONS",
		},
		{
			name: "bad statement timeout",
			env: with(map[string]string{
				"RELIC_DATABASE_STATEMENT_TIMEOUT": "soon",
			}),
			wantErr: "RELIC_DATABASE_STATEMENT_TIMEOUT",
		},
		{
			name: "failed sign-ins before backoff",
			env: with(map[string]string{
				"RELIC_FAILED_SIGN_INS_BEFORE_BACKOFF": "500",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.FailedSignInsBeforeBackoff != 500 {
					t.Errorf("FailedSignInsBeforeBackoff = %d", c.FailedSignInsBeforeBackoff)
				}
			},
		},
		{
			name: "sign-in note",
			env: with(map[string]string{
				"RELIC_SIGN_IN_NOTE": "Sign in as demo / demo. This demo is read-only.",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.SignInNote != "Sign in as demo / demo. This demo is read-only." {
					t.Errorf("SignInNote = %q", c.SignInNote)
				}
			},
		},
		{
			name: "no failed sign-ins before backoff",
			env: with(map[string]string{
				"RELIC_FAILED_SIGN_INS_BEFORE_BACKOFF": "0",
			}),
			wantErr: "RELIC_FAILED_SIGN_INS_BEFORE_BACKOFF",
		},
		{
			name: "bad max connections",
			env: with(map[string]string{
				"RELIC_MAX_CONNECTIONS": "-1",
			}),
			wantErr: "RELIC_MAX_CONNECTIONS",
		},
		{
			name: "invalid addr",
			env: with(map[string]string{
				"RELIC_HTTP_LISTEN_ADDRESS": "nope",
			}),
			wantErr: "RELIC_HTTP_LISTEN_ADDRESS",
		},
		{
			name: "missing database",
			env: with(map[string]string{
				"RELIC_DATABASE_URL": "",
			}),
			wantErr: "RELIC_DATABASE_URL",
		},
		{
			name: "relative s3 endpoint",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_ENDPOINT": "seaweed:8333",
			}),
			wantErr: "RELIC_OBJECT_STORAGE_ENDPOINT",
		},
		{
			name: "half a static key, access key missing",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_ACCESS_KEY": "",
			}),
			wantErr: "RELIC_OBJECT_STORAGE_SECRET_KEY",
		},
		{
			name: "same buckets",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_TEMPORARY_BUCKET": "artifacts",
			}),
			wantErr: "must differ",
		},
		{
			name: "filesystem storage needs no s3 settings",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_TYPE":       "filesystem",
				"RELIC_OBJECT_STORAGE_PATH":       "/var/lib/relic/objects",
				"RELIC_OBJECT_STORAGE_ENDPOINT":   "",
				"RELIC_OBJECT_STORAGE_ACCESS_KEY": "",
				"RELIC_OBJECT_STORAGE_SECRET_KEY": "",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.ObjectStorage.Type != "filesystem" || c.ObjectStorage.Path != "/var/lib/relic/objects" {
					t.Errorf("object storage = %+v", c.ObjectStorage)
				}
			},
		},
		{
			name: "relative filesystem path",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_TYPE": "filesystem",
				"RELIC_OBJECT_STORAGE_PATH": "objects",
			}),
			wantErr: "RELIC_OBJECT_STORAGE_PATH",
		},
		{
			name: "unknown storage type",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_TYPE": "ftp",
			}),
			wantErr: "RELIC_OBJECT_STORAGE_TYPE",
		},
		{
			name: "checksums only when required",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_CHECKSUM": "when_required",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.ObjectStorage.Checksum != "when_required" {
					t.Errorf("checksum = %q", c.ObjectStorage.Checksum)
				}
			},
		},
		{
			name: "upload limit with a unit",
			env: with(map[string]string{
				"RELIC_MAX_UPLOAD_SIZE": "10GiB",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.MaxUploadSize != 10<<30 {
					t.Errorf("MaxUploadSize = %d", c.MaxUploadSize)
				}
			},
		},
		{
			name: "upload limit in bytes",
			env: with(map[string]string{
				"RELIC_MAX_UPLOAD_SIZE": "1048576",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.MaxUploadSize != 1<<20 {
					t.Errorf("MaxUploadSize = %d", c.MaxUploadSize)
				}
			},
		},
		{
			name: "invalid upload limit",
			env: with(map[string]string{
				"RELIC_MAX_UPLOAD_SIZE": "10GB",
			}),
			wantErr: "RELIC_MAX_UPLOAD_SIZE",
		},
		{
			name: "unknown checksum setting",
			env: with(map[string]string{
				"RELIC_OBJECT_STORAGE_CHECKSUM": "never",
			}),
			wantErr: "RELIC_OBJECT_STORAGE_CHECKSUM",
		},
		{
			name: "switches in any spelling of true and false",
			env: with(map[string]string{
				"RELIC_TRASH_ENABLED":                    "FALSE",
				"RELIC_UPSTREAM_ALLOW_PRIVATE_ADDRESSES": "1",
				"RELIC_WEBHOOK_ALLOW_PRIVATE_ADDRESSES":  "True",
				"RELIC_SECRET_VALIDATION_ENABLED":        "true ",
			}),
			check: func(t *testing.T, c Config) {
				t.Helper()

				if c.Trash.Enabled || !c.AllowPrivateUpstreams || !c.AllowPrivateWebhooks || !c.Scan.SecretValidation {
					t.Errorf("trash %v, upstreams %v, webhooks %v, secret validation %v",
						c.Trash.Enabled, c.AllowPrivateUpstreams, c.AllowPrivateWebhooks, c.Scan.SecretValidation)
				}
			},
		},
		{
			name: "a switch that is neither true nor false",
			env: with(map[string]string{
				"RELIC_TRASH_ENABLED": "no",
			}),
			wantErr: "RELIC_TRASH_ENABLED",
		},
		{
			name: "a private address switch that is neither true nor false",
			env: with(map[string]string{
				"RELIC_UPSTREAM_ALLOW_PRIVATE_ADDRESSES": "yes",
			}),
			wantErr: "RELIC_UPSTREAM_ALLOW_PRIVATE_ADDRESSES",
		},
		{
			name: "a feed that is not a URL",
			env: with(map[string]string{
				"RELIC_CVE_KEV_URL": "not-a-url",
			}),
			wantErr: "RELIC_CVE_KEV_URL",
		},
		{
			name: "a feed turned off in capitals",
			env: with(map[string]string{
				"RELIC_CVE_EPSS_URL": "OFF",
			}),
			wantErr: "RELIC_CVE_EPSS_URL",
		},
		{
			name: "a vulnerability database listing that is not a URL",
			env: with(map[string]string{
				"RELIC_SCAN_VULNERABILITY_DATABASE_URL": "/srv/grype/latest.json",
			}),
			wantErr: "RELIC_SCAN_VULNERABILITY_DATABASE_URL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Load(func(k string) string {
				return tt.env[k]
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want containing %q", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			tt.check(t, cfg)
		})
	}
}
