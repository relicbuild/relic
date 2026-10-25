// Package servertls gives the server its TLS configuration: a certificate
// the operator provides, or one issued by a certificate authority Relic
// generates and keeps, with the certificate, in a directory.
package servertls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"relic/internal/platform/telemetry"
)

const (
	authorityLifetime   = 10 * 365 * 24 * time.Hour
	certificateLifetime = 90 * 24 * time.Hour
	renewBefore         = 30 * 24 * time.Hour
	clockSkew           = time.Hour
)

// The files a Directory keeps.
const (
	AuthorityCertificateFile = "ca.crt"
	AuthorityKeyFile         = "ca.key"
	CertificateFile          = "server.crt"
	KeyFile                  = "server.key"
)

// Config returns the server's TLS configuration from minVersion up: TLS 1.3
// with its default hybrid post-quantum key exchanges, TLS 1.2 with
// forward-secret AEAD suites only, and certificates from getCertificate.
func Config(minVersion uint16, getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{
		MinVersion: max(minVersion, tls.VersionTLS12),
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
		GetCertificate: getCertificate,
	}
}

// Authority is the certificate authority that issues the server's
// certificates.
type Authority struct {
	Certificate *x509.Certificate
	key         *ecdsa.PrivateKey
}

// NewAuthority generates a certificate authority: an ECDSA P-384 key and a
// self-signed certificate, signed with SHA-384, that may issue only end
// certificates.
func NewAuthority(now time.Time) (Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return Authority{}, fmt.Errorf("generate the certificate authority's key: %w", err)
	}

	serialNumber, err := randomSerialNumber()
	if err != nil {
		return Authority{}, err
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: "Relic server certificate authority",
		},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(authorityLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		SignatureAlgorithm:    x509.ECDSAWithSHA384,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return Authority{}, fmt.Errorf("create the certificate authority's certificate: %w", err)
	}

	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return Authority{}, fmt.Errorf("read the certificate authority's certificate: %w", err)
	}

	return Authority{
		Certificate: certificate,
		key:         key,
	}, nil
}

// CertificatePEM returns the authority's certificate as PEM, for clients to
// trust.
func (a Authority) CertificatePEM() []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: a.Certificate.Raw,
	})
}

// Fingerprint returns the SHA-256 digest of the authority's certificate as
// colon-separated hex pairs.
func (a Authority) Fingerprint() string {
	return Fingerprint(a.Certificate)
}

// Fingerprint returns the SHA-256 digest of a certificate as
// colon-separated hex pairs.
func Fingerprint(certificate *x509.Certificate) string {
	digest := sha256.Sum256(certificate.Raw)
	pairs := make([]string, len(digest))

	for i, b := range digest {
		pairs[i] = fmt.Sprintf("%02X", b)
	}

	return strings.Join(pairs, ":")
}

// Issue returns a server certificate for hostnames (names or IP
// addresses): an ECDSA P-384 key and a certificate signed with SHA-384,
// with the authority's certificate in its chain.
func (a Authority) Issue(hostnames []string, now time.Time) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate the server's key: %w", err)
	}

	serialNumber, err := randomSerialNumber()
	if err != nil {
		return nil, err
	}

	notAfter := now.Add(certificateLifetime)
	if notAfter.After(a.Certificate.NotAfter) {
		notAfter = a.Certificate.NotAfter
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: "Relic server",
		},
		NotBefore: now.Add(-clockSkew),
		NotAfter:  notAfter,
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		BasicConstraintsValid: true,
		SignatureAlgorithm:    x509.ECDSAWithSHA384,
	}

	for _, hostname := range hostnames {
		if address, err := netip.ParseAddr(hostname); err == nil {
			template.IPAddresses = append(template.IPAddresses, net.IP(address.AsSlice()))
		} else {
			template.DNSNames = append(template.DNSNames, hostname)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, template, a.Certificate, &key.PublicKey, a.key)
	if err != nil {
		return nil, fmt.Errorf("issue the server's certificate: %w", err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("read the server's certificate: %w", err)
	}

	return &tls.Certificate{
		Certificate: [][]byte{
			der,
			a.Certificate.Raw,
		},
		PrivateKey: key,
		Leaf:       leaf,
	}, nil
}

func randomSerialNumber() (*big.Int, error) {
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("generate a certificate serial number: %w", err)
	}

	return serialNumber, nil
}

// Directory keeps a generated certificate authority and the server
// certificate it issues, again at every start and before expiry, as PEM
// files.
type Directory struct {
	path      string
	authority Authority
	hostnames []string
	now       func() time.Time

	mu      sync.Mutex
	current *tls.Certificate
}

// OpenDirectory reads the certificate authority kept in path, generating it
// first when the directory has none, and issues a server certificate for
// hostnames.
func OpenDirectory(ctx context.Context, path string, hostnames []string, now func() time.Time) (*Directory, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("TLS directory: %w", err)
	}

	authority, err := loadAuthority(path, now())
	if err != nil {
		return nil, err
	}

	directory := &Directory{
		path:      path,
		authority: authority,
		hostnames: hostnames,
		now:       now,
	}

	if err := directory.issue(ctx); err != nil {
		return nil, err
	}

	return directory, nil
}

func loadAuthority(path string, now time.Time) (Authority, error) {
	certificatePath := filepath.Join(path, AuthorityCertificateFile)
	keyPath := filepath.Join(path, AuthorityKeyFile)

	pair, err := tls.LoadX509KeyPair(certificatePath, keyPath)
	if err == nil {
		key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
		if !ok {
			return Authority{}, fmt.Errorf("%s is a %T key, not ECDSA", keyPath, pair.PrivateKey)
		}

		return Authority{
			Certificate: pair.Leaf,
			key:         key,
		}, nil
	}

	_, certificateErr := os.Stat(certificatePath)
	_, keyErr := os.Stat(keyPath)

	if !errors.Is(certificateErr, fs.ErrNotExist) || !errors.Is(keyErr, fs.ErrNotExist) {
		return Authority{}, fmt.Errorf("TLS directory's certificate authority: %w", err)
	}

	authority, err := NewAuthority(now)
	if err != nil {
		return Authority{}, err
	}

	keyPEM, err := privateKeyPEM(authority.key)
	if err != nil {
		return Authority{}, err
	}

	if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
		return Authority{}, err
	}

	if err := writeFile(certificatePath, authority.CertificatePEM(), 0o644); err != nil {
		return Authority{}, err
	}

	return authority, nil
}

// Authority returns the directory's certificate authority.
func (d *Directory) Authority() Authority {
	return d.authority
}

// GetCertificate returns the current server certificate, issuing a new one
// when it is close to expiry; it is a tls.Config.GetCertificate.
func (d *Directory) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.now().Before(d.current.Leaf.NotAfter.Add(-renewBefore)) {
		return d.current, nil
	}

	ctx := context.Background()
	if hello != nil {
		ctx = hello.Context()
	}

	if err := d.issue(ctx); err != nil {
		return nil, err
	}

	return d.current, nil
}

func (d *Directory) issue(ctx context.Context) (err error) {
	_, span := telemetry.Tracer().Start(ctx, "tls.issue_certificate")

	defer func() {
		telemetry.TLSCertificateIssued(err)
		telemetry.End(span, err)
	}()

	issued, err := d.authority.Issue(d.hostnames, d.now())
	if err != nil {
		return err
	}

	keyPEM, err := privateKeyPEM(issued.PrivateKey)
	if err != nil {
		return err
	}

	var chainPEM []byte
	for _, der := range issued.Certificate {
		chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: der,
		})...)
	}

	if err := writeFile(filepath.Join(d.path, KeyFile), keyPEM, 0o600); err != nil {
		return err
	}

	if err := writeFile(filepath.Join(d.path, CertificateFile), chainPEM, 0o644); err != nil {
		return err
	}

	d.current = issued
	telemetry.TLSCertificateServed(telemetry.TLSSourceGenerated, issued.Leaf.NotAfter)

	return nil
}

func privateKeyPEM(key any) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode a private key: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	}), nil
}

func writeFile(path string, contents []byte, mode fs.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	defer func() {
		_ = os.Remove(temporary.Name())
	}()

	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("write %s: %w", path, err)
	}

	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()

		return fmt.Errorf("write %s: %w", path, err)
	}

	if err := temporary.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

// CertificateFiles serves a certificate and key the operator provides as
// PEM files, and reads them again when either file changes.
type CertificateFiles struct {
	certificatePath string
	keyPath         string

	log *slog.Logger

	mu       sync.Mutex
	current  *tls.Certificate
	modified [2]time.Time
	warned   string
}

// LoadCertificateFiles reads a PEM certificate chain and its PEM private
// key.
func LoadCertificateFiles(
	ctx context.Context,
	certificatePath, keyPath string,
	log *slog.Logger,
) (*CertificateFiles, error) {
	files := &CertificateFiles{
		certificatePath: certificatePath,
		keyPath:         keyPath,
		log:             log,
	}

	if _, err := files.certificate(ctx); err != nil {
		return nil, err
	}

	return files, nil
}

// GetCertificate returns the files' certificate, keeping the last one read
// while the files are being replaced; it is a tls.Config.GetCertificate.
func (f *CertificateFiles) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	ctx := context.Background()
	if hello != nil {
		ctx = hello.Context()
	}

	return f.certificate(ctx)
}

func (f *CertificateFiles) certificate(ctx context.Context) (*tls.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	modified, err := f.modifiedTimes()
	if err != nil {
		if f.current != nil {
			f.warn(ctx, err)
			return f.current, nil
		}

		return nil, err
	}

	if f.current != nil && modified == f.modified {
		return f.current, nil
	}

	loaded, err := tls.LoadX509KeyPair(f.certificatePath, f.keyPath)
	if err != nil {
		if f.current != nil {
			f.modified = modified
			f.warn(ctx, err)

			return f.current, nil
		}

		return nil, fmt.Errorf("RELIC_TLS_CERTIFICATE_FILE and RELIC_TLS_KEY_FILE: %w", err)
	}

	if f.current != nil {
		f.log.InfoContext(
			ctx,
			"TLS certificate files reloaded",
			"file", f.certificatePath,
			"not_after", loaded.Leaf.NotAfter,
		)
	}

	f.current = &loaded
	f.modified = modified
	f.warned = ""

	telemetry.TLSCertificateServed(telemetry.TLSSourceOperator, loaded.Leaf.NotAfter)

	return f.current, nil
}

func (f *CertificateFiles) warn(ctx context.Context, err error) {
	if err.Error() == f.warned {
		return
	}

	f.warned = err.Error()
	f.log.WarnContext(
		ctx,
		"TLS certificate files changed but cannot be loaded; serving the previous certificate",
		"file", f.certificatePath,
		"error", err,
	)
}

func (f *CertificateFiles) modifiedTimes() ([2]time.Time, error) {
	var modified [2]time.Time

	for i, path := range []string{
		f.certificatePath,
		f.keyPath,
	} {
		stat, err := os.Stat(path)
		if err != nil {
			return modified, fmt.Errorf("TLS certificate files: %w", err)
		}

		modified[i] = stat.ModTime()
	}

	return modified, nil
}

// LocalHostnames returns the names and addresses this machine answers to:
// localhost, the loopback addresses, its host name and its interfaces'
// addresses.
func LocalHostnames() []string {
	hostnames := []string{
		"localhost",
		"127.0.0.1",
		"::1",
	}

	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		hostnames = append(hostnames, hostname)
	}

	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return hostnames
	}

	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil || prefix.Addr().IsLoopback() || prefix.Addr().IsLinkLocalUnicast() {
			continue
		}

		hostnames = append(hostnames, prefix.Addr().String())
	}

	return hostnames
}
