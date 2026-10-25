// Package app wires Relic's dependencies and runs its subcommands.
package app

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"uuid"

	adapter "relic/api/adapters/artifactory"
	accessapi "relic/api/adapters/artifactory/access"
	"relic/api/adapters/artifactory/buildjson"
	"relic/api/adapters/artifactory/layout"
	scanapi "relic/api/adapters/artifactory/xray"
	"relic/api/apispec"
	"relic/api/mcpserver"
	"relic/api/packages"
	"relic/api/relicapi"
	"relic/api/scim"
	"relic/internal/access/audit"
	"relic/internal/access/auth"
	"relic/internal/access/ldap"
	"relic/internal/access/project"
	"relic/internal/access/security"
	"relic/internal/access/sso"
	"relic/internal/access/workload"
	"relic/internal/app/backgroundjobs"
	"relic/internal/app/config"
	"relic/internal/app/openapi"
	"relic/internal/app/ui"
	"relic/internal/artifacts/cleanup"
	"relic/internal/artifacts/formats/agentregistry"
	"relic/internal/artifacts/formats/alpine"
	"relic/internal/artifacts/formats/cargo"
	"relic/internal/artifacts/formats/conan"
	"relic/internal/artifacts/formats/debian"
	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/formats/gems"
	"relic/internal/artifacts/formats/gomod"
	"relic/internal/artifacts/formats/helm"
	"relic/internal/artifacts/formats/huggingface"
	"relic/internal/artifacts/formats/maven"
	"relic/internal/artifacts/formats/nix"
	"relic/internal/artifacts/formats/npm"
	"relic/internal/artifacts/formats/nuget"
	"relic/internal/artifacts/formats/pypi"
	"relic/internal/artifacts/formats/rpm"
	"relic/internal/artifacts/formats/terraform"
	"relic/internal/artifacts/repository"
	"relic/internal/artifacts/signingkey"
	usagestats "relic/internal/artifacts/usage"
	"relic/internal/delivery/buildinfo"
	"relic/internal/delivery/evidence"
	"relic/internal/delivery/release"
	"relic/internal/delivery/replication"
	"relic/internal/delivery/webhook"
	"relic/internal/platform/database"
	"relic/internal/platform/events"
	"relic/internal/platform/httperror"
	"relic/internal/platform/httpserver"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/sealing"
	"relic/internal/platform/servertls"
	"relic/internal/platform/telemetry"
	"relic/internal/scanning/cvecatalog"
	"relic/internal/scanning/endoflife"
	"relic/internal/scanning/malicious"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/blocking"
	"relic/internal/scanning/scan/policy"
	"relic/internal/systemstatus"

	evidenceapi "relic/api/adapters/artifactory/evidence"
	lifecycleapi "relic/api/adapters/artifactory/lifecycle"

	"github.com/riverqueue/river/rivertype"
	"golang.org/x/net/netutil"

	"github.com/riverqueue/river"
)

const usage = `usage: relic <command>

commands:
  server            apply migrations, then run the HTTP API and embedded UI
  server --worker   the server and the background jobs in one process
  server --no-tls   the server over plain HTTP instead of HTTPS
  worker            apply migrations, then run background jobs (GC, replication)
  migrate           apply database migrations and exit`

// Run executes the subcommand named by args[0] until ctx is cancelled.
func Run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}

	switch args[0] {
	case "server", "worker", "migrate":
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}

	var withWorker, withoutTLS bool

	for _, a := range args[1:] {
		switch {
		case a == "--worker" && args[0] == "server":
			withWorker = true
		case a == "--no-tls" && args[0] == "server":
			withoutTLS = true
		default:
			return fmt.Errorf("unknown argument %q\n%s", a, usage)
		}
	}

	if err := CheckSettings(getenv, args[0]); err != nil {
		return err
	}

	version, revision := relicVersion()

	tel, err := telemetry.Setup(
		ctx,
		telemetry.Options{
			Getenv:    getenv,
			Stderr:    stderr,
			Component: args[0],
			Version:   version,
			Revision:  revision,
		},
	)
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer func() {
		_ = tel.Shutdown(context.WithoutCancel(ctx))
	}()

	log := tel.Logger

	if limit := memoryLimit(getenv, cgroupMemoryMax); limit > 0 {
		debug.SetMemoryLimit(limit)
		log.InfoContext(ctx, "memory limit set from the container's", "bytes", limit)
	}

	log.InfoContext(ctx, "uploads to object storage in flight at most",
		"slots", objectstore.UploadSlots(debug.SetMemoryLimit(-1)))

	if args[0] != "migrate" {
		if _, err := tel.ServeMetrics(ctx); err != nil {
			return err
		}
	}

	switch args[0] {
	case "server":
		return runServer(ctx, getenv, serverMode{
			withWorker: withWorker,
			withoutTLS: withoutTLS,
		}, log)
	case "worker":
		return runWorker(ctx, getenv, log)
	default:
		return migrate(ctx, getenv, log)
	}
}

// CheckSettings returns every error in the settings command ("server",
// "worker" or "migrate") reads from getenv, or nil.
func CheckSettings(getenv func(string) string, command string) error {
	var errs []error

	if _, err := telemetry.LoadConfig(getenv, command); err != nil {
		errs = append(errs, fmt.Errorf("telemetry: %w", err))
	}

	if command != "migrate" {
		if _, err := config.Load(getenv); err != nil {
			errs = append(errs, fmt.Errorf("config: %w", err))
		}
	}

	return errors.Join(errs...)
}

func migrate(ctx context.Context, getenv func(string) string, log *slog.Logger) error {
	url := getenv("RELIC_DATABASE_URL")
	if url == "" {
		return errors.New("config: RELIC_DATABASE_URL is required")
	}

	db, err := database.Open(ctx, url)
	if err != nil {
		return err
	}
	defer db.Close()

	return migrateDB(ctx, db, log)
}

func migrateDB(ctx context.Context, db *database.DB, log *slog.Logger) error {
	applied, err := db.Migrate(ctx)
	if newer, ok := errors.AsType[*database.NewerSchemaError](err); ok {
		version, _ := relicVersion()
		return fmt.Errorf("relic %s cannot use this database: %w", version, newer)
	}

	if err != nil {
		return err
	}

	for _, m := range applied {
		log.InfoContext(ctx, "database migration applied", "version", m.Version, "name", m.Name, "duration", m.Duration)
	}

	return nil
}

func openDB(ctx context.Context, cfg config.Config, log *slog.Logger) (*database.DB, error) {
	db, err := database.OpenWith(ctx, cfg.DatabaseURL, database.Options{
		MaxConnections: cfg.DatabaseMaxConnections,
	})
	if err != nil {
		return nil, err
	}

	if err := migrateDB(ctx, db, log); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func openServerDB(ctx context.Context, cfg config.Config, log *slog.Logger) (*database.DB, error) {
	db, err := openDB(ctx, cfg, log)
	if err != nil || cfg.DatabaseStatementTimeout == 0 {
		return db, err
	}

	db.Close()

	return database.OpenWith(ctx, cfg.DatabaseURL, database.Options{
		StatementTimeout: cfg.DatabaseStatementTimeout,
		MaxConnections:   cfg.DatabaseMaxConnections,
	})
}

type serverMode struct {
	withWorker bool
	withoutTLS bool
}

func runServer(ctx context.Context, getenv func(string) string, mode serverMode, log *slog.Logger) (err error) {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	var tlsConfig *tls.Config
	if !mode.withoutTLS {
		if tlsConfig, err = serverTLSConfig(ctx, cfg.TLS, cfg.HTTPListenAddress, log); err != nil {
			return err
		}
	}

	db, err := openServerDB(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := BootstrapAdmin(ctx, db, cfg.AdminUsername, cfg.AdminPassword, log); err != nil {
		return err
	}

	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	blobs, err := blobStore(ctx, cfg, stop, log)
	if err != nil {
		return err
	}

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", cfg.HTTPListenAddress)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	if cfg.MaxConnections > 0 {
		ln = netutil.LimitListener(ln, cfg.MaxConnections)
	}

	box, err := sealing.New(cfg.EncryptionKey)
	if err != nil {
		return err
	}

	if err := SealStoredSecrets(ctx, db, blobs, box, log); err != nil {
		return err
	}

	tokens, err := NewTokens(ctx, db, box)
	if err != nil {
		return err
	}

	queue, err := backgroundjobs.NewInsertClient(db, log)
	if err != nil {
		return err
	}

	usageRecorder := usagestats.NewRecorder(db, log)
	go usageRecorder.Run(ctx, usagestats.FlushInterval)

	downloads := usagestats.NewItemDownloads(db, log)
	downloads.SetUsage(usageRecorder.Download)

	go downloads.Run(ctx)

	ldapTLS, err := ldapTLSConfig(cfg.LDAPCACertificateFile)
	if err != nil {
		return err
	}

	upstreamRoots, err := trustedRoots("RELIC_UPSTREAM_CA_CERTIFICATE_FILE", cfg.UpstreamCACertificateFile)
	if err != nil {
		return err
	}

	handler := NewHandler(db, blobs, tokens, queue, Options{
		IndexerDir:                 cfg.Scan.IndexerDir,
		AllowPrivateUpstreams:      cfg.AllowPrivateUpstreams,
		UpstreamRoots:              upstreamRoots,
		MaxUploadSize:              cfg.MaxUploadSize,
		Downloads:                  downloads,
		Usage:                      usageRecorder,
		DisableTrash:               !cfg.Trash.Enabled,
		Secrets:                    box,
		TrustedProxies:             cfg.TrustedProxies,
		LDAPTLS:                    ldapTLS,
		FailedSignInsBeforeBackoff: cfg.FailedSignInsBeforeBackoff,
		SignInNote:                 cfg.SignInNote,
	}, log)
	if mode.withWorker {
		workerDB, openErr := openDB(ctx, cfg, log)
		if openErr != nil {
			return openErr
		}
		defer workerDB.Close()

		stop, startErr := startWorker(ctx, cfg, workerDB, blobs, box, log)
		if startErr != nil {
			return startErr
		}

		// The worker stops after the server, which has drained its requests.
		defer func() {
			err = errors.Join(err, stop())
		}()
	}

	log.InfoContext(
		ctx,
		"relic server listening",
		"addr", ln.Addr().String(),
		"worker", mode.withWorker,
		"tls", tlsConfig != nil,
	)

	srv := httpserver.NewServer(cfg.HTTPListenAddress, handler, log)
	srv.TLSConfig = tlsConfig

	if err := httpserver.Serve(ctx, srv, ln); err != nil {
		return err
	}

	return stopCause(ctx)
}

func serverTLSConfig(ctx context.Context, cfg config.TLS, listenAddress string, log *slog.Logger) (*tls.Config, error) {
	if cfg.CertificateFile != "" {
		files, err := servertls.LoadCertificateFiles(ctx, cfg.CertificateFile, cfg.KeyFile, log)
		if err != nil {
			return nil, err
		}

		log.InfoContext(ctx, "serving HTTPS with the operator's certificate", "file", cfg.CertificateFile)

		return servertls.Config(cfg.MinVersion, files.GetCertificate), nil
	}

	if cfg.Directory == "" {
		return nil, errors.New("no TLS directory: set RELIC_TLS_DIRECTORY or HOME, " +
			"set RELIC_TLS_CERTIFICATE_FILE and RELIC_TLS_KEY_FILE, or start with --no-tls")
	}

	hostnames := servertls.LocalHostnames()
	if host, _, err := net.SplitHostPort(listenAddress); err == nil && host != "" && !slices.Contains(hostnames, host) {
		hostnames = append(hostnames, host)
	}

	directory, err := servertls.OpenDirectory(ctx, cfg.Directory, append(hostnames, cfg.Hostnames...), time.Now)
	if err != nil {
		return nil, err
	}

	log.InfoContext(
		ctx,
		"serving HTTPS with a certificate from Relic's certificate authority",
		"certificate_authority", filepath.Join(cfg.Directory, servertls.AuthorityCertificateFile),
		"sha256_fingerprint", directory.Authority().Fingerprint(),
	)

	return servertls.Config(cfg.MinVersion, directory.GetCertificate), nil
}

// NewTokens is the token service, issuing tokens in the format the clients
// of the compatible APIs read (the adapter's).
func NewTokens(ctx context.Context, db *database.DB, box *sealing.Box) (*auth.Tokens, error) {
	return auth.NewTokens(ctx, db, box, accessapi.Format{})
}

func ldapTLSConfig(caFile string) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	pool, err := trustedRoots("RELIC_LDAP_CA_CERTIFICATE_FILE", caFile)
	if err != nil {
		return nil, err
	}

	cfg.RootCAs = pool

	return cfg, nil
}

func trustedRoots(setting, file string) (*x509.CertPool, error) {
	if file == "" {
		return nil, nil //nolint:nilnil // nil selects the system roots
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}

	pem, err := os.ReadFile(file) //nolint:gosec // an operator-set path
	if err != nil {
		return nil, fmt.Errorf("%s: %w", setting, err)
	}

	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s %s holds no PEM certificates", setting, file)
	}

	return pool, nil
}

// SealStoredSecrets seals the secrets stored before RELIC_ENCRYPTION_KEY was
// set.
func SealStoredSecrets(
	ctx context.Context,
	db *database.DB,
	blobs objectstore.Store,
	box *sealing.Box,
	log *slog.Logger,
) error {
	if !box.Enabled() {
		log.WarnContext(
			ctx,
			"RELIC_ENCRYPTION_KEY is not set: signing keys, passwords and webhook secrets are stored unencrypted",
		)

		return nil
	}

	repos := newRepositories(db, blobs, log)
	repos.SetSecrets(box)

	if err := errors.Join(
		repos.SealStoredPasswords(ctx),
		signingkey.New(db, box).SealStored(ctx),
		replication.SealStored(ctx, db, box),
		ldap.NewService(db, box, nil, log).SealStored(ctx),
		sso.New(db, box, log).SealStored(ctx),
		webhook.New(db, box, log).SealStored(ctx),
	); err != nil {
		return err
	}

	return db.TruncateLog(ctx)
}

const layoutRetryInterval = 5 * time.Second

func blobStore(
	ctx context.Context,
	cfg config.Config,
	stop context.CancelCauseFunc,
	log *slog.Logger,
) (objectstore.Store, error) {
	blobs, err := objectstore.Open(ctx, objectstore.Config{
		Type: cfg.ObjectStorage.Type,
		S3: objectstore.S3Config{
			Endpoint:             cfg.ObjectStorage.Endpoint,
			Region:               cfg.ObjectStorage.Region,
			AccessKey:            cfg.ObjectStorage.AccessKey,
			SecretKey:            cfg.ObjectStorage.SecretKey,
			ArtifactsBucket:      cfg.ObjectStorage.ArtifactsBucket,
			TemporaryBucket:      cfg.ObjectStorage.TemporaryBucket,
			ChecksumWhenRequired: cfg.ObjectStorage.Checksum == "when_required",
			MaxConcurrentUploads: objectstore.UploadSlots(debug.SetMemoryLimit(-1)),
			ServerSideEncryption: cfg.ObjectStorage.ServerSideEncryption,
			SSEKMSKeyID:          cfg.ObjectStorage.SSEKMSKeyID,
			StorageClass:         cfg.ObjectStorage.StorageClass,
		},
		Azure: objectstore.AzureConfig{
			AccountURL:       cfg.ObjectStorage.Endpoint,
			ConnectionString: cfg.ObjectStorage.AzureConnectionString,
			ArtifactsBucket:  cfg.ObjectStorage.ArtifactsBucket,
			TemporaryBucket:  cfg.ObjectStorage.TemporaryBucket,
		},
		GCS: objectstore.GCSConfig{
			Endpoint:        cfg.ObjectStorage.Endpoint,
			ArtifactsBucket: cfg.ObjectStorage.ArtifactsBucket,
			TemporaryBucket: cfg.ObjectStorage.TemporaryBucket,
		},
		FilesystemPath: cfg.ObjectStorage.Path,
	})
	if err != nil {
		return nil, fmt.Errorf("object storage: %w", err)
	}

	err = objectstore.RecordLayout(ctx, blobs)
	if errors.Is(err, objectstore.ErrNoBucket) {
		log.WarnContext(
			ctx,
			"the object storage bucket does not exist yet: uploads fail until it does",
			"bucket", cfg.ObjectStorage.ArtifactsBucket,
		)

		go func() {
			awaitErr := objectstore.AwaitLayout(ctx, blobs, layoutRetryInterval)
			if awaitErr != nil && ctx.Err() == nil {
				stop(fmt.Errorf("object storage: %w", awaitErr))
			}
		}()

		return blobs, nil
	}

	if err != nil {
		return nil, fmt.Errorf("object storage: %w", err)
	}

	return blobs, nil
}

func stopCause(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}

	return nil
}

func runWorker(ctx context.Context, getenv func(string) string, log *slog.Logger) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	db, err := openDB(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	blobs, err := blobStore(ctx, cfg, stop, log)
	if err != nil {
		return err
	}

	box, err := sealing.New(cfg.EncryptionKey)
	if err != nil {
		return err
	}

	stopWorker, err := startWorker(ctx, cfg, db, blobs, box, log)
	if err != nil {
		return err
	}

	<-ctx.Done()

	return errors.Join(stopWorker(), stopCause(ctx))
}

func startWorker(
	ctx context.Context,
	cfg config.Config,
	db *database.DB,
	blobs objectstore.Store,
	box *sealing.Box,
	log *slog.Logger,
) (func() error, error) {
	upstreamRoots, err := trustedRoots("RELIC_UPSTREAM_CA_CERTIFICATE_FILE", cfg.UpstreamCACertificateFile)
	if err != nil {
		return nil, err
	}

	repos := newRepositories(db, blobs, log)
	repos.AllowPrivateUpstreams(cfg.AllowPrivateUpstreams)
	repos.TrustUpstreamRoots(upstreamRoots)
	repos.SetSecrets(box)
	repos.SetTrash(cfg.Trash.Enabled)

	pusher := replication.NewPusher(
		db,
		repos,
		adapter.ReplicationTargets(replicationClient(cfg.AllowPrivateUpstreams, upstreamRoots)),
		log,
	)
	pusher.SetSecrets(box)

	scanners := scanning(db, blobs, cfg.Scan, log)

	webhooks := webhook.New(db, box, log)
	webhooks.SetHTTPClient(webhookClient(cfg.AllowPrivateWebhooks))
	repos.SetEvents(events.Fanout{
		webhooks,
		replication.NewChanges(pusher, nil, log),
	})

	if scanners != nil {
		scanners.Service.SetEvents(webhooks)
	}

	client, err := backgroundjobs.NewWorkerClient(
		db,
		repos,
		pusher,
		scanners,
		webhooks,
		cfg.GarbageCollection,
		cfg.Trash,
		cfg.Audit,
		log,
	)
	if err != nil {
		return nil, err
	}

	go backgroundjobs.Monitor{
		DB:           db,
		Repositories: repos,
		Kinds:        backgroundjobs.Kinds(scanners != nil),
		Log:          log,
	}.Run(ctx)

	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("start worker: %w", err)
	}

	log.InfoContext(ctx, "relic server worker started")

	return func() error {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()

		return client.Stop(stopCtx)
	}, nil
}

// Queue enqueues background jobs for the worker (a River client).
type Queue interface {
	Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

func enqueueReplication(queue Queue) func(context.Context, string) error {
	if queue == nil {
		return nil
	}

	return func(ctx context.Context, repoKey string) error {
		_, err := queue.Insert(ctx, backgroundjobs.ReplicationArgs{
			Repository: repoKey,
		}, nil)

		return err
	}
}

func enqueueStorageCheck(queue Queue) func(context.Context) (int64, error) {
	if queue == nil {
		return nil
	}

	return func(ctx context.Context) (int64, error) {
		res, err := queue.Insert(ctx, backgroundjobs.StorageCheckArgs{}, nil)
		if err != nil {
			return 0, err
		}

		return res.Job.ID, nil
	}
}

const webhookTimeout = 10 * time.Second

func outboundTransport(allowPrivate bool) *http.Transport {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	if !allowPrivate {
		transport.DialContext = repository.PublicAddressDialer().DialContext
		transport.Proxy = nil
	}

	return transport
}

func replicationClient(allowPrivate bool, roots *x509.CertPool) *http.Client {
	transport := outboundTransport(allowPrivate)
	if roots != nil {
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		}
	}

	return &http.Client{
		Transport: telemetry.ClientTransport(transport),
	}
}

func webhookClient(allowPrivate bool) *http.Client {
	return &http.Client{
		Timeout:   webhookTimeout,
		Transport: telemetry.ClientTransport(outboundTransport(allowPrivate)),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func nudgeWebhooks(queue Queue, log *slog.Logger) func(context.Context) {
	if queue == nil {
		return nil
	}

	return func(ctx context.Context) {
		if _, err := queue.Insert(ctx, backgroundjobs.WebhookDispatchArgs{}, nil); err != nil {
			log.WarnContext(ctx, "queue webhook dispatch", "error", err)
		}
	}
}

func enqueueGC(queue Queue) func(context.Context) error {
	if queue == nil {
		return nil
	}

	return func(ctx context.Context) error {
		_, err := queue.Insert(ctx, backgroundjobs.GCArgs{}, &river.InsertOpts{
			UniqueOpts: river.UniqueOpts{
				ByState: []rivertype.JobState{
					rivertype.JobStateAvailable,
					rivertype.JobStatePending,
					rivertype.JobStateRunning,
					rivertype.JobStateRetryable,
					rivertype.JobStateScheduled,
				},
			},
		})

		return err
	}
}

func enqueueGraphScan(queue Queue) func(context.Context, uuid.UUID) error {
	return func(ctx context.Context, id uuid.UUID) error {
		if queue == nil {
			return errors.New("no job queue")
		}

		_, err := queue.Insert(ctx, backgroundjobs.GraphScanArgs{
			ID: id,
		}, nil)

		return err
	}
}

// River reports a queue it works every ten minutes.
const queueReportWindow = 20 * time.Minute

func queueWorkers(status *systemstatus.Service, queue string) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		queues, err := status.Queues(ctx)
		if err != nil {
			return false, err
		}

		return slices.ContainsFunc(queues, func(q systemstatus.Queue) bool {
			return q.Name == queue && time.Since(q.Updated) < queueReportWindow
		}), nil
	}
}

func enqueueRematch(queue Queue) func(context.Context, string) error {
	if queue == nil {
		return nil
	}

	return func(ctx context.Context, sha256Hex string) error {
		_, err := queue.Insert(ctx, backgroundjobs.RematchArgs{
			SHA256: sha256Hex,
		}, nil)

		return err
	}
}

func enqueueOperationalRiskCheck(queue Queue) func(context.Context) error {
	return func(ctx context.Context) error {
		if queue == nil {
			return errors.New("no job queue")
		}

		_, err := queue.Insert(ctx, backgroundjobs.OperationalRiskArgs{}, nil)

		return err
	}
}

func enqueueDatabaseImport(queue Queue) func(context.Context, string) error {
	return func(ctx context.Context, key string) error {
		if queue == nil {
			return errors.New("no job queue")
		}

		_, err := queue.Insert(ctx, backgroundjobs.ScanDBImportArgs{
			Key: key,
		}, nil)

		return err
	}
}

// Options are optional server settings.
type Options struct {
	IndexerDir string
	// AllowPrivateUpstreams lets remotes reach private addresses.
	AllowPrivateUpstreams bool
	// UpstreamRoots are the CA certificates remotes trust; nil for the
	// system roots.
	UpstreamRoots *x509.CertPool
	// MaxUploadSize caps every upload in bytes; 0 for no limit.
	MaxUploadSize int64
	// Downloads counts downloads; nil counts nothing (tests).
	Downloads *usagestats.ItemDownloads
	// Usage counts downloads and deploys by day; nil counts nothing (tests).
	Usage *usagestats.Recorder
	// DisableTrash makes user deletes permanent (RELIC_TRASH_ENABLED=false).
	DisableTrash bool
	// Secrets seals stored credentials and keys; nil stores them as they are.
	Secrets *sealing.Box
	// TrustedProxies may set X-Forwarded-* headers; none when empty.
	TrustedProxies []netip.Prefix
	// FailedSignInsBeforeBackoff is how many failed password attempts a
	// client address gets before it has to wait; 0 keeps the default.
	FailedSignInsBeforeBackoff int
	// SignInNote is shown on the sign-in page; none when empty.
	SignInNote string
	// LDAPTLS verifies LDAP servers (ldaps:// and StartTLS); nil uses the
	// system roots.
	LDAPTLS *tls.Config
}

// NewHandler builds the full HTTP handler: middleware plus every API.
func NewHandler(
	db *database.DB,
	blobs objectstore.Store,
	tokens *auth.Tokens,
	queue Queue,
	opts Options,
	log *slog.Logger,
) http.Handler {
	rt, authn := newRouter(db, blobs, tokens, queue, opts, log)

	adapter.RegisterPaths()
	packages.RegisterPaths()
	scim.RegisterPaths()
	// The server span wraps everything after the client address is known,
	// so Observe's request log line carries its trace ID.
	return httpserver.TrustProxies(
		opts.TrustedProxies,
		telemetry.HTTPServer(httpserver.HSTS(httpserver.Observe(log, audit.Middleware(authn.Middleware(rt))))),
	)
}

func newRouter(
	db *database.DB,
	blobs objectstore.Store,
	tokens *auth.Tokens,
	queue Queue,
	opts Options,
	log *slog.Logger,
) (*httpserver.Router, *auth.Authenticator) {
	checks := healthChecks(db, blobs)
	rt := routes(checks)
	auditLog := audit.New(db, log)
	rt.SetAuditHook(recordRoute(auditLog))

	workloads := workload.New(db, tokens, log)
	workloads.SetAudit(auditLog)

	repos := newRepositories(db, blobs, log)
	repos.AllowPrivateUpstreams(opts.AllowPrivateUpstreams)
	repos.TrustUpstreamRoots(opts.UpstreamRoots)
	repos.SetMaxUploadSize(opts.MaxUploadSize)
	repos.SetTrash(!opts.DisableTrash)
	repos.SetSecrets(opts.Secrets)
	sec := security.NewService(db, log, repos.TrashRepository())
	projects := project.New(db)
	projects.SetRepositoryProjects(repos.Projects)
	projects.SetChanged(sec.Invalidate)
	sec.SetProjectPermissions(projects.Permissions)
	sec.SetOwnRepositories(repos.OwnRepositories)
	sec.RegisterPrincipalRemovalHook(projects.RemoveMemberships)
	repos.SetProjectQuota(projects.Quota)

	authn := auth.NewAuthenticator(db, sec, tokens, log)
	authn.SetPasswordSealing(opts.Secrets)

	if opts.FailedSignInsBeforeBackoff > 0 {
		authn.SetFailedSignInsBeforeBackoff(opts.FailedSignInsBeforeBackoff)
	}

	adapter.ConfigureAuth(authn)
	packages.ConfigureAuth(authn)

	builds := buildinfo.NewService(db, repos, buildjson.Format{}, log)
	builds.SetLayout(layout.BuildInfo)
	repos.ReserveStorage(builds.StorageOwner)
	h := adapter.New(repos, builds, sec, tokens, db, log)
	h.SetReplicationQueue(enqueueReplication(queue))
	h.SetSecrets(opts.Secrets)
	replications := replication.NewService(db, opts.Secrets)
	h.SetReplications(replications)

	dockerSvc := docker.NewService(repos, blobs, log)
	npmSvc := npm.NewService(db, repos, log)
	pypiSvc, helmSvc := pypi.NewService(repos, log), helm.NewService(repos, log)
	keySvc := signingkey.New(db, opts.Secrets)
	blocker := blocking.New(db)
	blocker.SetAudit(auditLog)
	blocker.SetQuarantine(repos)

	agentRegistry := agentregistry.New(db, repos)
	blocker.SetRegistryApprovals(agentRegistry)

	policyDecisions := &policy.Decisions{
		DB:  db,
		Log: log,
	}
	blocker.SetDecisions(policyDecisions)

	webhooks := webhook.New(db, opts.Secrets, log)
	webhooks.SetNudge(nudgeWebhooks(queue, log))
	repos.SetEvents(events.Fanout{
		webhooks,
		replication.NewChanges(replication.NewPusher(db, repos, nil, log), enqueueReplication(queue), log),
		opts.Usage,
	})
	blocker.SetEvents(webhooks)
	repos.SetVersionGate(blocker.VersionGate(malicious.NewCatalog(db), repos))
	pkgs := packages.New(packages.Dependencies{
		Repositories:       repos,
		Security:           sec,
		Tokens:             tokens,
		Authenticator:      authn,
		Npm:                npmSvc,
		Maven:              maven.NewService(repos, log),
		Docker:             dockerSvc,
		Go:                 gomod.NewService(repos, log),
		PyPI:               pypiSvc,
		Helm:               helmSvc,
		NuGet:              nuget.NewService(repos, log),
		Gems:               gems.NewService(repos, log),
		Cargo:              cargo.NewService(repos, log),
		Conan:              conan.NewService(repos, log),
		HuggingFace:        huggingface.NewService(repos, log),
		Alpine:             alpine.NewService(repos, keySvc, log),
		Debian:             debian.NewService(db, repos, keySvc, log),
		RPM:                rpm.NewService(repos, keySvc, log),
		Nix:                nix.NewService(repos, log),
		TerraformProviders: terraform.NewService(repos, log),
		Blocker:            blocker,
		Downloads:          opts.Downloads,
		Log:                log,
	})
	h.SetPackages(pkgs, dockerSvc, keySvc)
	h.SetDownloads(opts.Downloads)

	scanAdmin := &policy.Admin{
		DB:      db,
		Changed: blocker.Invalidate,
	}

	h.Register(rt)
	pkgs.Register(rt, h.RegistryMount())

	bundles := release.New(db, repos, builds, keySvc, log)
	bundles.SetLayout(layout.ReleaseBundles)
	bundles.SetProjects(projects)
	projects.SetStorageRepositories(func(key string) []string {
		return []string{
			builds.Repository(key),
			bundles.Layout().Repository(key),
		}
	})
	repos.ReserveStorage(bundles.StorageOwner)
	bundles.SetDownloadBlocker(blocker.RefuseCopy)
	builds.SetDownloadBlocker(blocker.RefuseCopy)
	lifecycleapi.New(bundles, sec, log).Register(rt)
	evidenceSvc := evidence.New(db, repos, log)
	bundles.SetEvidence(evidenceSvc)
	bundles.SetDecisions(policyDecisions)
	bundles.SetEvents(webhooks)
	evidenceapi.New(evidenceSvc, bundles, repos, sec, log).Register(rt)

	logins := auth.NewWebLogins(db, tokens)
	ldapSvc := ldap.NewService(db, opts.Secrets, opts.LDAPTLS, log)
	ah := accessapi.New(tokens, logins, log)
	ah.SetLDAP(ldapSvc)
	ah.SetSecurity(sec)
	ah.SetWorkloads(workloads)
	ah.SetProjects(projects)
	authn.SetExternal(ldapSvc)
	ah.Register(rt)

	scans := scan.NewResults(db)
	graphScans := scan.NewService(db, blobs, nil, nil, scan.Limits{}, log)
	xh := scanapi.New(
		db,
		scan.NewResults(db),
		builds,
		graphScans,
		scanAdmin,
		enqueueGraphScan(queue),
		log,
	)
	xh.IndexerDir = opts.IndexerDir
	xh.Permissions = sec.Permissions
	xh.SetRepos(repos)
	xh.SetDecisions(policyDecisions)
	xh.Register(rt)
	scim.New(sso.NewDirectory(db, sec), log).Register(rt)

	status := systemstatus.NewService(db)
	version, revision := relicVersion()
	api := relicapi.Handler(relicapi.Dependencies{ // each method enforces its own access
		Version:                          version,
		Revision:                         revision,
		SignInNote:                       opts.SignInNote,
		Authenticator:                    authn,
		Tokens:                           tokens,
		WebLogins:                        logins,
		Audit:                            auditLog,
		SSO:                              sso.New(db, opts.Secrets, log),
		Workloads:                        workloads,
		Repositories:                     repos,
		Builds:                           builds,
		Security:                         sec,
		LDAP:                             ldapSvc,
		Releases:                         bundles,
		Evidence:                         evidenceSvc,
		SigningKeys:                      keySvc,
		ClientEndpoints:                  packages.ClientEndpoints,
		Replications:                     replications,
		Cleanup:                          cleanup.New(db, repos, log),
		ScanResults:                      scans,
		Scans:                            graphScans,
		CVEs:                             cvecatalog.NewCatalog(db),
		ScanPolicies:                     scanAdmin,
		PolicyDecisions:                  policyDecisions,
		Webhooks:                         webhooks,
		Projects:                         projects,
		AgentRegistry:                    agentRegistry,
		Usage:                            usagestats.NewService(db, repos.Projects),
		SystemStatus:                     status,
		DownloadBlocker:                  blocker,
		DownloadStats:                    opts.Downloads,
		ContainerImages:                  dockerSvc,
		NpmPackages:                      npmSvc,
		PythonPackages:                   pypiSvc,
		MaliciousPackages:                malicious.NewCatalog(db),
		EndOfLife:                        endoflife.NewCatalog(db, nil),
		EndOfLifeData:                    endoflife.NewRefresher(db, "", nil, log),
		QueueOperationalRiskCheck:        enqueueOperationalRiskCheck(queue),
		HelmCharts:                       helmSvc,
		HealthChecks:                     checks,
		QueueGarbageCollection:           enqueueGC(queue),
		QueueStorageCheck:                enqueueStorageCheck(queue),
		QueueReplication:                 enqueueReplication(queue),
		QueueGraphScan:                   enqueueGraphScan(queue),
		GraphScanWorkers:                 queueWorkers(status, backgroundjobs.GraphQueue),
		ScanWorkers:                      queueWorkers(status, backgroundjobs.ScanQueue),
		QueueRematch:                     enqueueRematch(queue),
		VulnerabilityDatabases:           scan.NewDatabaseImports(blobs, log),
		QueueVulnerabilityDatabaseImport: enqueueDatabaseImport(queue),
		Log:                              log,
		Interceptors:                     telemetry.ConnectInterceptors(),
	})
	rt.Handle(relicapi.Prefix, auth.Public, api)
	rt.Handle(mcpserver.Path, auth.Authenticated, mcpserver.Handler(api, version))
	rt.HandleFunc("GET "+openAPIPath, auth.Public, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(apispec.JSON)
	})

	return rt, authn
}

var routesInfo = openapi.Info{
	Title:   "Relic HTTP routes",
	Version: "1",
	Description: "Every HTTP route the Relic server registers, with its access level: Relic's own and the " +
		"compatibility adapter's (api/adapters/artifactory). An internal inventory for tests; the Relic API's " +
		"description is api/apispec.",
	Tag: adapter.RouteTag,
}

const openAPIPath = "/api/v1/openapi.json"

// RouteInventory returns every route the server registers, as an OpenAPI
// document.
func RouteInventory() ([]byte, error) {
	rt, _ := newRouter(nil, nil, nil, nil, Options{}, slog.New(slog.DiscardHandler))
	return openapi.Marshal(openapi.Build(rt.Routes(), nil, routesInfo))
}

type blobHeader interface {
	Head(ctx context.Context, key string) (objectstore.Info, error)
}

const readyProbeKey = "readyz/probe"

func storageCheck(blobs blobHeader) func(context.Context) error {
	return func(ctx context.Context) error {
		if _, err := blobs.Head(ctx, readyProbeKey); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
			return err
		}

		return nil
	}
}

func healthChecks(db interface{ Ping(context.Context) error }, blobs blobHeader) []relicapi.HealthCheck {
	return []relicapi.HealthCheck{
		{
			Name:  "database",
			Check: db.Ping,
		},
		{
			Name:  "object storage",
			Check: storageCheck(blobs),
		},
	}
}

func recordRoute(auditLog *audit.Service) httpserver.AuditHook {
	return func(r *http.Request, route httpserver.Route, status int) {
		path := route.Pattern
		if _, p, ok := strings.Cut(path, " "); ok {
			path = p
		}

		e := audit.Event{
			Actor:   auth.FromContext(r.Context()).Name(),
			Action:  r.Method + " " + path,
			Target:  cmp.Or(httpserver.AuditTarget(r.Context()), r.URL.Path),
			Outcome: audit.HTTPOutcome(status),
		}
		if status >= http.StatusBadRequest {
			e.Detail = http.StatusText(status)
		}

		auditLog.Record(r.Context(), e)
	}
}

func routes(checks []relicapi.HealthCheck) *httpserver.Router {
	rt := httpserver.NewRouter()
	rt.HandleFunc("GET /healthz", auth.Public, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	rt.HandleFunc("GET /readyz", auth.Public, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		for _, c := range checks {
			if err := c.Check(ctx); err != nil {
				http.Error(w, c.Name+" unavailable", http.StatusServiceUnavailable)
				return
			}
		}

		_, _ = io.WriteString(w, "ok")
	})
	// The pattern has no method, since a method would make it conflict with
	// the method-less API prefixes.
	rt.Handle("/", auth.Public, uiHeaders(apiNotFound(ui.Handler("/"))))

	return rt
}

func scanning(db *database.DB, blobs objectstore.Store, cfg config.Scan, log *slog.Logger) *backgroundjobs.Scanning {
	syftPath, err1 := exec.LookPath(cfg.SyftPath)

	grypePath, err2 := exec.LookPath(cfg.GrypePath)
	if err := errors.Join(err1, err2); err != nil {
		log.Warn("Relic Scan disabled: scanner binaries not found", "error", err)
		return nil
	}

	grype := scan.Grype{
		Exec: scan.Exec{
			Path: grypePath,
		},
		DatabaseDir: cfg.VulnerabilityDatabaseDir,
		UpdateURL:   cfg.VulnerabilityDatabaseURL,
		Offline:     cfg.VulnerabilityDatabaseOff,
	}
	syft := scan.Syft{
		Path: syftPath,
	}
	feeds := cvecatalog.Feeds{
		EPSS:    cfg.EPSSFeedURL,
		KEV:     cfg.KEVFeedURL,
		Records: cfg.CVERecordsURL,
	}

	svc := scan.NewService(db, blobs, syft, grype, scan.Limits{}, log)
	svc.SetMalicious(malicious.NewCatalog(db))
	svc.SetAnalyzers(analyzers(cfg, log)...)

	if cfg.SecretValidation {
		log.Warn("Relic Scan validates the secrets it finds with their providers (RELIC_SECRET_VALIDATION_ENABLED)")
		svc.SetSecretValidator(scan.NewSecretValidator(cfg.SecretValidationProviders, audit.New(db, log)))
	}

	var feed *malicious.Refresher
	if cfg.MaliciousPackagesURL != "" {
		feed = malicious.NewRefresher(db, cfg.MaliciousPackagesURL, nil, log)
	}

	var deps *endoflife.DepsDev
	if cfg.DepsDevURL != "" {
		deps = endoflife.NewDepsDev(db, cfg.DepsDevURL, nil, log)
	}

	svc.SetOperationalRisk(endoflife.NewCatalog(db, deps))

	var goDB *scan.GoVulnerabilityDatabase

	if p, err := exec.LookPath(cfg.GovulncheckPath); err != nil {
		log.Warn("Go reachability off: govulncheck not found", "error", err)
	} else {
		goDB = &scan.GoVulnerabilityDatabase{
			Dir: filepath.Join(cfg.VulnerabilityDatabaseDir, "go"),
			URL: cfg.GoVulnerabilityDatabaseURL,
		}
		svc.SetReachability(&scan.GoReachability{
			Exec: scan.Exec{
				Path:    p,
				Timeout: 5 * time.Minute,
			},
			DatabaseDir: goDB.Dir,
		})
	}

	return &backgroundjobs.Scanning{
		Service:           svc,
		Grype:             grype,
		Imports:           scan.NewDatabaseImports(blobs, log),
		Malicious:         feed,
		EndOfLife:         endoflife.NewRefresher(db, cfg.EndOfLifeURL, nil, log),
		GoVulnerabilities: goDB,
		CVEs:              cvecatalog.NewRefresher(db, feeds, nil, log),
	}
}

func analyzers(cfg config.Scan, log *slog.Logger) []scan.Analyzer {
	out := []scan.Analyzer{
		scan.Model{},
	}

	if p, err := exec.LookPath(cfg.GitleaksPath); err == nil {
		out = append(out, scan.Gitleaks{
			Exec: scan.Exec{
				Path: p,
			},
			Unredacted: cfg.SecretValidation,
		})
	} else {
		log.Warn("Relic Scan: secrets analysis off: gitleaks not found", "error", err)
	}

	if p, err := exec.LookPath(cfg.TrivyPath); err == nil {
		out = append(out, scan.Trivy{
			Exec: scan.Exec{
				Path: p,
			},
			CacheDir: filepath.Join(cfg.VulnerabilityDatabaseDir, "trivy"),
		})
	} else {
		log.Warn("Relic Scan: misconfiguration analysis off: trivy not found", "error", err)
	}

	if cfg.ScanCodePath != "" {
		p, err := exec.LookPath(cfg.ScanCodePath)
		if err != nil {
			log.Warn("Relic Scan: deep license scanning off: scancode not found", "error", err)

			return out
		}

		out = append(out, scan.ScanCode{
			Exec: scan.Exec{
				Path:    p,
				Timeout: 20 * time.Minute,
			},
		})
	}

	return out
}

// BootstrapAdmin creates the first administrator when there are no users.
func BootstrapAdmin(ctx context.Context, db *database.DB, username, password string, log *slog.Logger) error {
	n, err := db.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}

	if n > 0 {
		return nil
	}

	if password == "" {
		return errors.New("no users exist: set RELIC_ADMIN_PASSWORD to create the first administrator")
	}

	if !security.ValidUsername(username) {
		return fmt.Errorf("RELIC_ADMIN_USERNAME: %w", security.InvalidUsername(username))
	}

	if err := security.CheckPassword(password); err != nil {
		return fmt.Errorf("RELIC_ADMIN_PASSWORD: %w", err)
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	err = db.CreateUser(
		ctx,
		database.CreateUserParams{
			ID:           uuid.NewV7(),
			Username:     username,
			PasswordHash: hash,
			Admin:        true,
		},
	)
	if err != nil && !database.IsUniqueViolation(err) {
		return fmt.Errorf("create admin: %w", err)
	}

	log.InfoContext(ctx, "created bootstrap administrator", "username", username)

	return nil
}

var apiPrefixes = append([]string{
	"/api/",
	scim.PathPrefix,
	packages.RegistryV1Prefix,
}, adapter.PathPrefixes...)

func apiNotFound(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range apiPrefixes {
			if strings.HasPrefix(r.URL.Path, p) || r.URL.Path == strings.TrimSuffix(p, "/") {
				httperror.Write(w, r, http.StatusNotFound, "no such endpoint")
				return
			}
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func uiHeaders(next http.Handler) http.Handler {
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'wasm-unsafe-eval' " + strings.Join(ui.ScriptHashes(), " "),
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"worker-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// Version is the release the binary was built as; empty uses the module version from the build info.
var Version string

// Revision is the commit the binary was built from; empty uses the build info's VCS revision.
var Revision string

func relicVersion() (version, revision string) {
	version = "dev"
	if Version != "" {
		version = Version
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; Version == "" && v != "" && v != "(devel)" {
			version = v
		}

		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				revision = s.Value
			}
		}
	}

	if Revision != "" {
		revision = Revision
	}

	if m := describedCommit.FindStringSubmatch(version); revision == "" && m != nil {
		revision = m[1]
	}

	return version, revision
}

// describedCommit is the commit git describe --long appends to a version: v1.2.3-4-gabc1234.
var describedCommit = regexp.MustCompile(`-g([0-9a-f]{7,40})$`)

func newRepositories(db *database.DB, blobs objectstore.Store, log *slog.Logger) *repository.Service {
	repos := repository.NewService(db, blobs, log)
	repos.SetTrashLayout(layout.Trash)

	return repos
}
