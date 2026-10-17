// Package config loads and validates Relic's runtime configuration.
package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"relic/internal/platform/httpserver"
	"relic/internal/platform/sealing"
	"relic/internal/scanning/scan"
)

// Config is the effective runtime configuration.
type Config struct {
	// HTTPListenAddress is the listen address of the API server, e.g. ":8090".
	HTTPListenAddress string
	// DatabaseURL is a PostgreSQL connection URL.
	DatabaseURL   string
	ObjectStorage ObjectStorage
	// AdminUsername and AdminPassword create the first administrator when no
	// user exists yet.
	AdminUsername     string
	AdminPassword     string
	Scan              Scan
	GarbageCollection GarbageCollection
	// AllowPrivateUpstreams lets remote repositories and replication reach
	// loopback and private addresses (RELIC_UPSTREAM_ALLOW_PRIVATE_ADDRESSES=true).
	AllowPrivateUpstreams bool
	// AllowPrivateWebhooks lets webhooks reach loopback and private addresses
	// (RELIC_WEBHOOK_ALLOW_PRIVATE_ADDRESSES=true).
	AllowPrivateWebhooks bool
	Trash                Trash
	Audit                Audit
	// EncryptionKey seals secrets stored in the database (RELIC_ENCRYPTION_KEY,
	// 32 bytes in base64).
	EncryptionKey []byte
	// DatabaseStatementTimeout bounds every server query
	// (RELIC_DATABASE_STATEMENT_TIMEOUT, default 5m; 0 turns it off).
	DatabaseStatementTimeout time.Duration
	// DatabaseMaxConnections sizes each process's PostgreSQL connection
	// pool (RELIC_DATABASE_MAX_CONNECTIONS, default 20).
	DatabaseMaxConnections int32
	// MaxUploadSize caps every upload in bytes (RELIC_MAX_UPLOAD_SIZE, a
	// number of bytes or with KiB, MiB, GiB or TiB; 0, the default, for no
	// limit).
	MaxUploadSize int64
	// MaxConnections caps concurrent client connections
	// (RELIC_MAX_CONNECTIONS, default 8192; 0 turns it off).
	MaxConnections int
	// FailedSignInsBeforeBackoff is how many failed password attempts a
	// client address gets within ten minutes before each further one makes
	// it wait (RELIC_FAILED_SIGN_INS_BEFORE_BACKOFF, default 20; threat
	// model M3).
	FailedSignInsBeforeBackoff int
	// TrustedProxies are the reverse proxies whose X-Forwarded-* headers
	// are believed (RELIC_TRUSTED_PROXIES, comma-separated CIDRs or
	// addresses).
	TrustedProxies []netip.Prefix
	// SignInNote is plain text the sign-in page shows, such as the
	// credentials of a demo (RELIC_SIGN_IN_NOTE).
	SignInNote string
	// LDAPCACertificateFile is a PEM file of CA certificates trusted, besides the
	// system roots, for LDAP servers over ldaps:// and StartTLS
	// (RELIC_LDAP_CA_CERTIFICATE_FILE).
	LDAPCACertificateFile string
	TLS                   TLS
}

// TLS configures HTTPS on the server's listen address, which serves plain
// HTTP only when the server starts with --no-tls.
type TLS struct {
	// CertificateFile and KeyFile are the operator's PEM certificate chain
	// and key (RELIC_TLS_CERTIFICATE_FILE, RELIC_TLS_KEY_FILE).
	CertificateFile string
	KeyFile         string
	// Directory keeps the certificate authority Relic generates and the
	// certificate it issues, when the operator provides none
	// (RELIC_TLS_DIRECTORY, default $XDG_CONFIG_HOME/relic/tls or
	// ~/.config/relic/tls).
	Directory string
	// Hostnames are names and addresses the issued certificate holds
	// besides the machine's own (RELIC_TLS_HOSTNAMES, comma separated).
	Hostnames []string
	// MinVersion is the oldest TLS version clients may use
	// (RELIC_TLS_MIN_VERSION, 1.2 or 1.3; default 1.2).
	MinVersion uint16
}

// Trash configures the trash can: user deletes move items there, and the
// worker purges them after the retention period.
type Trash struct {
	// Enabled is RELIC_TRASH_ENABLED (default true; "false" makes deletes
	// permanent).
	Enabled bool
	// Retention is RELIC_TRASH_RETENTION (default 336h, 14 days).
	Retention time.Duration
}

// Audit configures the audit log.
type Audit struct {
	// Retention is RELIC_AUDIT_RETENTION (default 8760h, 365 days); the worker
	// purges older events.
	Retention time.Duration
}

// GarbageCollection tunes blob garbage collection in the worker.
type GarbageCollection struct {
	Interval time.Duration // RELIC_GARBAGE_COLLECTION_INTERVAL, default 10m
	// GracePeriod is how long unreferenced content is kept (RELIC_GARBAGE_COLLECTION_GRACE_PERIOD,
	// default 1h).
	GracePeriod time.Duration
}

// Scan configures Relic Scan's scanners.
type Scan struct {
	SyftPath  string // RELIC_SCAN_SYFT_PATH, default "syft" on PATH
	GrypePath string // RELIC_SCAN_GRYPE_PATH, default "grype" on PATH
	// GitleaksPath and TrivyPath run the secrets and misconfiguration
	// analyzers; each is off when its binary is not found.
	GitleaksPath string // RELIC_SCAN_GITLEAKS_PATH, default "gitleaks" on PATH
	TrivyPath    string // RELIC_SCAN_TRIVY_PATH, default "trivy" on PATH
	// GovulncheckPath (RELIC_SCAN_GOVULNCHECK_PATH, default "govulncheck" on
	// PATH) tells which vulnerable functions Go executables call.
	GovulncheckPath string
	// GoVulnerabilityDatabaseURL (RELIC_SCAN_GO_VULNERABILITY_DATABASE_URL)
	// is the Go vulnerability database archive (vuln.go.dev/vulndb.zip) or a
	// copy of it; "off" downloads none.
	GoVulnerabilityDatabaseURL string
	// ScanCodePath (RELIC_SCAN_SCANCODE_PATH) runs deep license scanning:
	// the licenses in every file's text.
	ScanCodePath             string
	VulnerabilityDatabaseDir string // RELIC_SCAN_VULNERABILITY_DATABASE_DIR: Grype's vulnerability database
	// VulnerabilityDatabaseURL (RELIC_SCAN_VULNERABILITY_DATABASE_URL) is
	// where the worker downloads databases: unset for Grype's own listing,
	// a mirror of it, or "off" to download none.
	VulnerabilityDatabaseURL string
	VulnerabilityDatabaseOff bool
	// IndexerDir holds relic-indexer builds named relic-indexer-<os>-<arch>
	// (RELIC_INDEXER_DIR), served to scanning clients.
	IndexerDir string
	// CVE feeds the worker reads for CVE facts.
	EPSSFeedURL   string // RELIC_CVE_EPSS_URL: FIRST's daily EPSS scores (csv.gz)
	KEVFeedURL    string // RELIC_CVE_KEV_URL: CISA's KEV catalog (JSON)
	CVERecordsURL string // RELIC_CVE_RECORDS_URL: <url>/<CVE-ID> answers CVE JSON 5
	// MaliciousPackagesURL (RELIC_MALICIOUS_PACKAGES_URL) holds OSV's
	// per-ecosystem archives (<url>/<ecosystem>/all.zip), whose MAL-
	// records name malicious packages; "off" turns the feed off.
	MaliciousPackagesURL string
	// EndOfLifeURL (RELIC_END_OF_LIFE_URL) is endoflife.date's full product
	// dump or a mirror of it; "off" reads none, and an import still can.
	EndOfLifeURL string
	// DepsDevURL (RELIC_DEPS_DEV_URL) turns on deprecation and maintenance
	// checks against deps.dev's API (https://api.deps.dev) or a mirror.
	// Unset or "off" keeps them off: they send package names to it.
	DepsDevURL string
	// SecretValidation (RELIC_SECRET_VALIDATION_ENABLED=true) checks the
	// secrets found in content with their providers; off by default, since
	// it sends each secret to its provider.
	SecretValidation bool
	// SecretValidationProviders (RELIC_SECRET_VALIDATION_PROVIDERS, comma
	// separated) narrows the providers asked; empty asks all of them.
	SecretValidationProviders []string
}

// ObjectStorage locates the object store: an S3-compatible service
// (SeaweedFS's S3 gateway), Azure Blob Storage, Google Cloud
// Storage or a local directory.
type ObjectStorage struct {
	Type string // s3, azure, gcs or filesystem
	Path string // the filesystem store's directory
	// Endpoint is the S3-compatible service, the Azure storage account's
	// blob URL, or a Google Cloud Storage emulator; empty for AWS S3 and
	// Google Cloud Storage themselves.
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	// ArtifactsBucket and TemporaryBucket are buckets, or Azure containers.
	ArtifactsBucket string
	TemporaryBucket string
	Checksum        string // RELIC_OBJECT_STORAGE_CHECKSUM: when_supported or when_required
	// ServerSideEncryption is S3's AES256 or aws:kms, with SSEKMSKeyID.
	ServerSideEncryption string
	SSEKMSKeyID          string
	StorageClass         string
	// AzureConnectionString authenticates to Azure instead of the default
	// credential (workload identity, managed identity, the environment).
	AzureConnectionString string
}

// Load builds a Config from environment variables, applying defaults.
func Load(getenv func(string) string) (Config, error) {
	env := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}

		return def
	}

	var errs []error

	switchOn := func(key string, def bool) bool {
		on, err := parseSwitch(getenv(key), def)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %q must be true or false", key, getenv(key)))
		}

		return on
	}
	cfg := Config{
		HTTPListenAddress: env("RELIC_HTTP_LISTEN_ADDRESS", ":8090"),
		DatabaseURL:       getenv("RELIC_DATABASE_URL"),
		ObjectStorage: ObjectStorage{
			Type:            env("RELIC_OBJECT_STORAGE_TYPE", "s3"),
			Path:            getenv("RELIC_OBJECT_STORAGE_PATH"),
			Endpoint:        getenv("RELIC_OBJECT_STORAGE_ENDPOINT"),
			Region:          env("RELIC_OBJECT_STORAGE_REGION", "us-east-1"),
			AccessKey:       getenv("RELIC_OBJECT_STORAGE_ACCESS_KEY"),
			SecretKey:       getenv("RELIC_OBJECT_STORAGE_SECRET_KEY"),
			ArtifactsBucket: env("RELIC_OBJECT_STORAGE_ARTIFACTS_BUCKET", "artifacts"),
			TemporaryBucket: env("RELIC_OBJECT_STORAGE_TEMPORARY_BUCKET", "temporary"),
			Checksum:        env("RELIC_OBJECT_STORAGE_CHECKSUM", "when_supported"),

			ServerSideEncryption:  getenv("RELIC_OBJECT_STORAGE_SSE"),
			SSEKMSKeyID:           getenv("RELIC_OBJECT_STORAGE_SSE_KMS_KEY_ID"),
			StorageClass:          getenv("RELIC_OBJECT_STORAGE_STORAGE_CLASS"),
			AzureConnectionString: getenv("RELIC_OBJECT_STORAGE_AZURE_CONNECTION_STRING"),
		},
		AdminUsername:         env("RELIC_ADMIN_USERNAME", "admin"),
		AdminPassword:         getenv("RELIC_ADMIN_PASSWORD"),
		AllowPrivateUpstreams: switchOn("RELIC_UPSTREAM_ALLOW_PRIVATE_ADDRESSES", false),
		AllowPrivateWebhooks:  switchOn("RELIC_WEBHOOK_ALLOW_PRIVATE_ADDRESSES", false),
		Trash: Trash{
			Enabled: switchOn("RELIC_TRASH_ENABLED", true),
		},
		Scan: Scan{
			SyftPath:                 env("RELIC_SCAN_SYFT_PATH", "syft"),
			GrypePath:                env("RELIC_SCAN_GRYPE_PATH", "grype"),
			GitleaksPath:             env("RELIC_SCAN_GITLEAKS_PATH", "gitleaks"),
			TrivyPath:                env("RELIC_SCAN_TRIVY_PATH", "trivy"),
			ScanCodePath:             getenv("RELIC_SCAN_SCANCODE_PATH"),
			VulnerabilityDatabaseDir: env("RELIC_SCAN_VULNERABILITY_DATABASE_DIR", "/var/lib/relic/grype"),
			VulnerabilityDatabaseURL: vulnerabilityDatabaseURL(getenv),
			VulnerabilityDatabaseOff: getenv("RELIC_SCAN_VULNERABILITY_DATABASE_URL") == "off",
			IndexerDir:               env("RELIC_INDEXER_DIR", "/usr/local/share/relic/indexer"),
			EPSSFeedURL: feed(
				getenv,
				"RELIC_CVE_EPSS_URL",
				"https://epss.empiricalsecurity.com/epss_scores-current.csv.gz",
			),
			KEVFeedURL: feed(
				getenv,
				"RELIC_CVE_KEV_URL",
				"https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json",
			),
			CVERecordsURL: feed(getenv, "RELIC_CVE_RECORDS_URL", "https://cveawg.mitre.org/api/cve"),
			MaliciousPackagesURL: feed(
				getenv,
				"RELIC_MALICIOUS_PACKAGES_URL",
				"https://osv-vulnerabilities.storage.googleapis.com",
			),
			EndOfLifeURL:     feed(getenv, "RELIC_END_OF_LIFE_URL", "https://endoflife.date/api/v1/products/full"),
			SecretValidation: switchOn("RELIC_SECRET_VALIDATION_ENABLED", false),
			GovulncheckPath:  env("RELIC_SCAN_GOVULNCHECK_PATH", "govulncheck"),
			GoVulnerabilityDatabaseURL: feed(
				getenv,
				"RELIC_SCAN_GO_VULNERABILITY_DATABASE_URL",
				"https://vuln.go.dev/vulndb.zip",
			),
			DepsDevURL: feed(getenv, "RELIC_DEPS_DEV_URL", ""),
		},
	}

	for _, f := range []struct {
		key string
		url string
	}{
		{
			"RELIC_SCAN_VULNERABILITY_DATABASE_URL",
			cfg.Scan.VulnerabilityDatabaseURL,
		},
		{
			"RELIC_SCAN_GO_VULNERABILITY_DATABASE_URL",
			cfg.Scan.GoVulnerabilityDatabaseURL,
		},
		{
			"RELIC_CVE_EPSS_URL",
			cfg.Scan.EPSSFeedURL,
		},
		{
			"RELIC_CVE_KEV_URL",
			cfg.Scan.KEVFeedURL,
		},
		{
			"RELIC_CVE_RECORDS_URL",
			cfg.Scan.CVERecordsURL,
		},
		{
			"RELIC_MALICIOUS_PACKAGES_URL",
			cfg.Scan.MaliciousPackagesURL,
		},
		{
			"RELIC_END_OF_LIFE_URL",
			cfg.Scan.EndOfLifeURL,
		},
		{
			"RELIC_DEPS_DEV_URL",
			cfg.Scan.DepsDevURL,
		},
	} {
		if u, err := url.Parse(f.url); f.url != "" &&
			(err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "") {
			errs = append(errs, fmt.Errorf("%s %q must be an http or https URL, or off", f.key, f.url))
		}
	}

	for _, d := range []struct {
		key  string
		def  time.Duration
		dest *time.Duration
	}{
		{
			"RELIC_GARBAGE_COLLECTION_INTERVAL",
			10 * time.Minute,
			&cfg.GarbageCollection.Interval,
		},
		{
			"RELIC_GARBAGE_COLLECTION_GRACE_PERIOD",
			time.Hour,
			&cfg.GarbageCollection.GracePeriod,
		},
		{
			"RELIC_TRASH_RETENTION",
			14 * 24 * time.Hour,
			&cfg.Trash.Retention,
		},
		{
			"RELIC_AUDIT_RETENTION",
			365 * 24 * time.Hour,
			&cfg.Audit.Retention,
		},
	} {
		*d.dest = d.def
		if v := getenv(d.key); v != "" {
			parsed, err := time.ParseDuration(v)
			if err != nil || parsed <= 0 {
				errs = append(errs, fmt.Errorf("%s %q must be a positive duration", d.key, v))
				continue
			}

			*d.dest = parsed
		}
	}

	for p := range strings.SplitSeq(getenv("RELIC_SECRET_VALIDATION_PROVIDERS"), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if !slices.Contains(scan.SecretProviders, p) {
			errs = append(errs, fmt.Errorf(
				"RELIC_SECRET_VALIDATION_PROVIDERS names %q: use %s",
				p,
				strings.Join(scan.SecretProviders, ", "),
			))

			continue
		}

		cfg.Scan.SecretValidationProviders = append(cfg.Scan.SecretValidationProviders, p)
	}

	cfg.DatabaseStatementTimeout = 5 * time.Minute

	if v := getenv("RELIC_DATABASE_STATEMENT_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			errs = append(
				errs,
				fmt.Errorf("RELIC_DATABASE_STATEMENT_TIMEOUT %q must be a duration (0 turns it off)", v),
			)
		}

		cfg.DatabaseStatementTimeout = d
	}

	cfg.DatabaseMaxConnections = 20

	if v := getenv("RELIC_DATABASE_MAX_CONNECTIONS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("RELIC_DATABASE_MAX_CONNECTIONS %q must be a positive number", v))
		}

		cfg.DatabaseMaxConnections = int32(n)
	}

	if v := getenv("RELIC_MAX_UPLOAD_SIZE"); v != "" {
		n, err := parseSize(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("RELIC_MAX_UPLOAD_SIZE %q must be a size such as 10GiB: %w", v, err))
		}

		cfg.MaxUploadSize = n
	}

	cfg.MaxConnections = 8192

	if v := getenv("RELIC_MAX_CONNECTIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			errs = append(
				errs,
				fmt.Errorf("RELIC_MAX_CONNECTIONS %q must be a non-negative number (0 turns it off)", v),
			)
		}

		cfg.MaxConnections = n
	}

	cfg.FailedSignInsBeforeBackoff = 20

	if v := getenv("RELIC_FAILED_SIGN_INS_BEFORE_BACKOFF"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("RELIC_FAILED_SIGN_INS_BEFORE_BACKOFF %q must be a positive number", v))
		}

		cfg.FailedSignInsBeforeBackoff = n
	}

	proxies, err := httpserver.ParsePrefixes(getenv("RELIC_TRUSTED_PROXIES"))
	if err != nil {
		errs = append(errs, fmt.Errorf("RELIC_TRUSTED_PROXIES: %w", err))
	}

	cfg.TrustedProxies = proxies
	cfg.SignInNote = getenv("RELIC_SIGN_IN_NOTE")

	cfg.LDAPCACertificateFile = getenv("RELIC_LDAP_CA_CERTIFICATE_FILE")
	cfg.TLS = TLS{
		CertificateFile: getenv("RELIC_TLS_CERTIFICATE_FILE"),
		KeyFile:         getenv("RELIC_TLS_KEY_FILE"),
		Directory:       env("RELIC_TLS_DIRECTORY", defaultTLSDirectory(getenv)),
	}

	switch v := env("RELIC_TLS_MIN_VERSION", "1.2"); v {
	case "1.2":
		cfg.TLS.MinVersion = tls.VersionTLS12
	case "1.3":
		cfg.TLS.MinVersion = tls.VersionTLS13
	default:
		errs = append(errs, fmt.Errorf("RELIC_TLS_MIN_VERSION %q must be 1.2 or 1.3", v))
	}

	for hostname := range strings.SplitSeq(getenv("RELIC_TLS_HOSTNAMES"), ",") {
		if hostname = strings.TrimSpace(hostname); hostname == "" {
			continue
		}

		if _, err := netip.ParseAddr(hostname); err != nil && !isHostname(hostname) {
			errs = append(errs, fmt.Errorf("RELIC_TLS_HOSTNAMES %q must be a host name or an IP address", hostname))

			continue
		}

		cfg.TLS.Hostnames = append(cfg.TLS.Hostnames, hostname)
	}

	if v := getenv("RELIC_ENCRYPTION_KEY"); v != "" {
		key, err := sealing.ParseKey(v)
		if err != nil {
			errs = append(errs, err)
		}

		cfg.EncryptionKey = key
	}

	return cfg, errors.Join(append(errs, cfg.validate())...)
}

func (c Config) validate() error {
	var errs []error
	if _, _, err := net.SplitHostPort(c.HTTPListenAddress); err != nil {
		errs = append(errs, fmt.Errorf("RELIC_HTTP_LISTEN_ADDRESS %q: %w", c.HTTPListenAddress, err))
	}

	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("RELIC_DATABASE_URL is required"))
	}

	errs = append(errs, c.ObjectStorage.validate()...)
	errs = append(errs, c.TLS.validate()...)

	return errors.Join(errs...)
}

func (t TLS) validate() []error {
	var errs []error
	if (t.CertificateFile == "") != (t.KeyFile == "") {
		errs = append(errs, errors.New("set both RELIC_TLS_CERTIFICATE_FILE and RELIC_TLS_KEY_FILE, or neither"))
	}

	if t.Directory != "" && !filepath.IsAbs(t.Directory) {
		errs = append(errs, fmt.Errorf("RELIC_TLS_DIRECTORY %q must be an absolute path", t.Directory))
	}

	return errs
}

// isHostname reports whether name is a DNS name a certificate can hold,
// optionally a wildcard one such as *.example.com.
func isHostname(name string) bool {
	name = strings.TrimPrefix(name, "*.")
	if name == "" || len(name) > 253 {
		return false
	}

	for label := range strings.SplitSeq(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}

		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}

	return true
}

func defaultTLSDirectory(getenv func(string) string) string {
	if configHome := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(configHome) {
		return filepath.Join(configHome, "relic", "tls")
	}

	if home := getenv("HOME"); filepath.IsAbs(home) {
		return filepath.Join(home, ".config", "relic", "tls")
	}

	return ""
}

func (o ObjectStorage) validate() []error {
	switch o.Type {
	case "filesystem":
		if !filepath.IsAbs(o.Path) {
			return []error{
				fmt.Errorf("RELIC_OBJECT_STORAGE_PATH %q must be an absolute path", o.Path),
			}
		}

		return nil
	case "s3", "gcs":
	case "azure":
		if o.Endpoint == "" && o.AzureConnectionString == "" {
			return []error{
				errors.New("RELIC_OBJECT_STORAGE_ENDPOINT (the account's blob URL) or " +
					"RELIC_OBJECT_STORAGE_AZURE_CONNECTION_STRING is required for azure"),
			}
		}
	default:
		return []error{
			fmt.Errorf("RELIC_OBJECT_STORAGE_TYPE %q must be s3, azure, gcs or filesystem", o.Type),
		}
	}

	var errs []error
	if u, err := url.Parse(o.Endpoint); o.Endpoint != "" && (err != nil || u.Host == "") {
		errs = append(
			errs,
			fmt.Errorf("RELIC_OBJECT_STORAGE_ENDPOINT %q must be an absolute URL", o.Endpoint),
		)
	}

	if (o.AccessKey == "") != (o.SecretKey == "") {
		errs = append(
			errs,
			errors.New("set both RELIC_OBJECT_STORAGE_ACCESS_KEY and RELIC_OBJECT_STORAGE_SECRET_KEY, or neither "+
				"to use the AWS default credentials"),
		)
	}

	switch o.ServerSideEncryption {
	case "", "AES256":
	case "aws:kms":
	default:
		errs = append(
			errs,
			fmt.Errorf("RELIC_OBJECT_STORAGE_SSE %q must be AES256 or aws:kms", o.ServerSideEncryption),
		)
	}

	if o.SSEKMSKeyID != "" && o.ServerSideEncryption != "aws:kms" {
		errs = append(errs, errors.New("RELIC_OBJECT_STORAGE_SSE_KMS_KEY_ID needs RELIC_OBJECT_STORAGE_SSE=aws:kms"))
	}

	if o.Checksum != "when_supported" && o.Checksum != "when_required" {
		errs = append(
			errs,
			fmt.Errorf("RELIC_OBJECT_STORAGE_CHECKSUM %q must be when_supported or when_required", o.Checksum),
		)
	}

	if o.ArtifactsBucket == o.TemporaryBucket {
		errs = append(
			errs,
			errors.New("RELIC_OBJECT_STORAGE_ARTIFACTS_BUCKET and RELIC_OBJECT_STORAGE_TEMPORARY_BUCKET must differ"),
		)
	}

	return errs
}

func parseSwitch(v string, def bool) (bool, error) {
	if v = strings.TrimSpace(v); v == "" {
		return def, nil
	}

	on, err := strconv.ParseBool(v)
	if err != nil {
		return def, err
	}

	return on, nil
}

func feed(getenv func(string) string, key, def string) string {
	switch v := getenv(key); v {
	case "":
		return def
	case "off":
		return ""
	default:
		return v
	}
}

func vulnerabilityDatabaseURL(getenv func(string) string) string {
	if v := getenv("RELIC_SCAN_VULNERABILITY_DATABASE_URL"); v != "off" {
		return v
	}

	return ""
}

var sizeUnits = []struct {
	suffix string
	bytes  int64
}{
	{
		"TiB",
		1 << 40,
	},
	{
		"GiB",
		1 << 30,
	},
	{
		"MiB",
		1 << 20,
	},
	{
		"KiB",
		1 << 10,
	},
}

func parseSize(v string) (int64, error) {
	unit := int64(1)

	for _, u := range sizeUnits {
		if n, ok := strings.CutSuffix(v, u.suffix); ok {
			v, unit = n, u.bytes

			break
		}
	}

	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/unit {
		return 0, errors.New("not a size")
	}

	return n * unit, nil
}
