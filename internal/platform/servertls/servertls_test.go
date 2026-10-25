package servertls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var issueTime = time.Date(
	2026,
	10,
	14,
	8,
	0,
	0,
	0,
	time.UTC,
)

func TestAuthorityIssue(t *testing.T) {
	t.Parallel()

	authority, err := NewAuthority(issueTime)
	if err != nil {
		t.Fatal(err)
	}

	issued, err := authority.Issue([]string{
		"relic.example.com",
		"127.0.0.1",
		"::1",
	}, issueTime)
	if err != nil {
		t.Fatal(err)
	}

	if len(issued.Certificate) != 2 {
		t.Fatalf("chain has %d certificates, want the server's and the authority's", len(issued.Certificate))
	}

	leaf, err := x509.ParseCertificate(issued.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(authority.CertificatePEM())

	for _, name := range []string{
		"relic.example.com",
		"127.0.0.1",
		"::1",
	} {
		if _, err := leaf.Verify(x509.VerifyOptions{
			DNSName:     name,
			Roots:       roots,
			CurrentTime: issueTime,
			KeyUsages: []x509.ExtKeyUsage{
				x509.ExtKeyUsageServerAuth,
			},
		}); err != nil {
			t.Errorf("verify for %s: %v", name, err)
		}
	}

	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:     "other.example.com",
		Roots:       roots,
		CurrentTime: issueTime,
	}); err == nil {
		t.Error("the certificate verifies for a name it was not issued for")
	}

	for _, certificate := range []*x509.Certificate{
		leaf,
		authority.Certificate,
	} {
		key, ok := certificate.PublicKey.(*ecdsa.PublicKey)
		if !ok || key.Curve != elliptic.P384() {
			t.Errorf("%s: key %T, want ECDSA P-384", certificate.Subject.CommonName, certificate.PublicKey)
		}

		if certificate.SignatureAlgorithm != x509.ECDSAWithSHA384 {
			t.Errorf(
				"%s: signed with %v, want ECDSA with SHA-384",
				certificate.Subject.CommonName,
				certificate.SignatureAlgorithm,
			)
		}
	}

	if !authority.Certificate.IsCA || !authority.Certificate.MaxPathLenZero {
		t.Error("the authority's certificate is not a CA limited to issuing server certificates")
	}

	if leaf.IsCA {
		t.Error("the server's certificate is a CA")
	}
}

func TestOpenDirectoryKeepsItsAuthority(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "tls")
	clock := func() time.Time {
		return issueTime
	}

	first, err := OpenDirectory(t.Context(), dir, []string{
		"localhost",
	}, clock)
	if err != nil {
		t.Fatal(err)
	}

	again, err := OpenDirectory(t.Context(), dir, []string{
		"localhost",
	}, clock)
	if err != nil {
		t.Fatal(err)
	}

	if first.Authority().Fingerprint() != again.Authority().Fingerprint() {
		t.Error("opening the directory again made a new certificate authority")
	}

	for name, wantMode := range map[string]os.FileMode{
		AuthorityCertificateFile: 0o644,
		AuthorityKeyFile:         0o600,
		CertificateFile:          0o644,
		KeyFile:                  0o600,
	} {
		stat, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}

		if stat.Mode().Perm() != wantMode {
			t.Errorf("%s has mode %v, want %v", name, stat.Mode().Perm(), wantMode)
		}
	}

	served, err := tls.LoadX509KeyPair(filepath.Join(dir, CertificateFile), filepath.Join(dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}

	current, err := again.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	if !served.Leaf.Equal(current.Leaf) {
		t.Error("server.crt is not the certificate the server serves")
	}

	if err := os.Remove(filepath.Join(dir, AuthorityKeyFile)); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenDirectory(t.Context(), dir, nil, clock); err == nil {
		t.Error("a directory with ca.crt but no ca.key got a new certificate authority")
	}
}

func TestDirectoryRenewsBeforeExpiry(t *testing.T) {
	t.Parallel()

	now := issueTime

	dir, err := OpenDirectory(t.Context(), t.TempDir(), []string{
		"localhost",
	}, func() time.Time {
		return now
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := dir.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	now = issueTime.Add(certificateLifetime - renewBefore - time.Hour)

	if again, _ := dir.GetCertificate(nil); again != first {
		t.Error("a certificate far from expiry was issued again")
	}

	now = issueTime.Add(certificateLifetime - renewBefore + time.Hour)

	renewed, err := dir.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	if renewed == first || !renewed.Leaf.NotAfter.After(first.Leaf.NotAfter) {
		t.Error("a certificate close to expiry was not renewed")
	}
}

func TestCertificateFilesReloadWhenChanged(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certificatePath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")

	writeKeyPair(t, certificatePath, keyPath, "first.example.com", issueTime)

	files, err := LoadCertificateFiles(t.Context(), certificatePath, keyPath, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	first, err := files.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	if first.Leaf.DNSNames[0] != "first.example.com" {
		t.Fatalf("served %v", first.Leaf.DNSNames)
	}

	writeKeyPair(t, certificatePath, keyPath, "second.example.com", issueTime.Add(time.Hour))

	second, err := files.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	if second.Leaf.DNSNames[0] != "second.example.com" {
		t.Errorf("after the files changed, served %v", second.Leaf.DNSNames)
	}
}

func TestCertificateFilesLogReloads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certificatePath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")

	writeKeyPair(t, certificatePath, keyPath, "first.example.com", issueTime)

	var logged bytes.Buffer

	files, err := LoadCertificateFiles(
		t.Context(),
		certificatePath,
		keyPath,
		slog.New(slog.NewTextHandler(&logged, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}

	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(certificatePath, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	brokenAt := time.Now().Add(time.Minute)
	touch(t, certificatePath, brokenAt)

	for range 3 {
		served, err := files.GetCertificate(nil)
		if err != nil || served.Leaf.DNSNames[0] != "first.example.com" {
			t.Fatalf("with a broken certificate file, served %v, %v; want the previous certificate", served, err)
		}
	}

	if n := strings.Count(logged.String(), "TLS certificate files changed but cannot be loaded"); n != 1 {
		t.Errorf("a broken renewal logged %d warnings, want 1: %s", n, logged.String())
	}

	writeKeyPair(t, certificatePath, keyPath, "unchanged.example.com", issueTime)
	touch(t, certificatePath, brokenAt)
	touch(t, keyPath, keyInfo.ModTime())

	if served, _ := files.GetCertificate(nil); served.Leaf.DNSNames[0] != "first.example.com" {
		t.Errorf("files whose times did not change were read again: served %v", served.Leaf.DNSNames)
	}

	writeKeyPair(t, certificatePath, keyPath, "second.example.com", issueTime.Add(time.Hour))
	touch(t, certificatePath, time.Now().Add(2*time.Minute))

	served, err := files.GetCertificate(nil)
	if err != nil || served.Leaf.DNSNames[0] != "second.example.com" {
		t.Fatalf("after the renewal was fixed, served %v, %v", served, err)
	}

	if !strings.Contains(logged.String(), "TLS certificate files reloaded") {
		t.Errorf("a successful reload was not logged: %s", logged.String())
	}
}

func touch(t *testing.T, path string, modified time.Time) {
	t.Helper()

	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCertificateFilesRejectsAMismatchedKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certificatePath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	otherKeyPath := filepath.Join(dir, "other.key")

	writeKeyPair(t, certificatePath, keyPath, "relic.example.com", issueTime)
	writeKeyPair(t, filepath.Join(dir, "other.crt"), otherKeyPath, "relic.example.com", issueTime)

	if _, err := LoadCertificateFiles(
		t.Context(),
		certificatePath,
		otherKeyPath,
		slog.New(slog.DiscardHandler),
	); err == nil {
		t.Error("a key that does not match the certificate was accepted")
	}
}

func TestConfigVersions(t *testing.T) {
	t.Parallel()

	dir, err := OpenDirectory(t.Context(), t.TempDir(), []string{
		"127.0.0.1",
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	addresses := map[uint16]string{}
	for _, minVersion := range []uint16{
		tls.VersionTLS12,
		tls.VersionTLS13,
	} {
		addresses[minVersion] = serveTLS(t, Config(minVersion, dir.GetCertificate))
	}

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(dir.Authority().CertificatePEM())

	tests := []struct {
		name             string
		serverMinVersion uint16
		maxVersion       uint16
		cipherSuites     []uint16
		wantVersion      uint16
		wantError        bool
	}{
		{
			name:             "TLS 1.3",
			serverMinVersion: tls.VersionTLS12,
			maxVersion:       tls.VersionTLS13,
			wantVersion:      tls.VersionTLS13,
		},
		{
			name:             "TLS 1.2",
			serverMinVersion: tls.VersionTLS12,
			maxVersion:       tls.VersionTLS12,
			wantVersion:      tls.VersionTLS12,
		},
		{
			name:             "TLS 1.2 to a server that needs 1.3",
			serverMinVersion: tls.VersionTLS13,
			maxVersion:       tls.VersionTLS12,
			wantError:        true,
		},
		{
			name:             "TLS 1.3 to a server that needs 1.3",
			serverMinVersion: tls.VersionTLS13,
			maxVersion:       tls.VersionTLS13,
			wantVersion:      tls.VersionTLS13,
		},
		{
			name:             "TLS 1.2 with CBC suites only",
			serverMinVersion: tls.VersionTLS12,
			maxVersion:       tls.VersionTLS12,
			cipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			},
			wantError: true,
		},
		{
			name:             "TLS 1.1",
			serverMinVersion: tls.VersionTLS12,
			maxVersion:       tls.VersionTLS11,
			wantError:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dialer := &tls.Dialer{
				//nolint:gosec // G402: the client offers the old versions the server must refuse
				Config: &tls.Config{
					RootCAs:      roots,
					ServerName:   "127.0.0.1",
					MinVersion:   tls.VersionTLS10,
					MaxVersion:   tt.maxVersion,
					CipherSuites: tt.cipherSuites,
				},
			}

			conn, err := dialer.DialContext(t.Context(), "tcp", addresses[tt.serverMinVersion])
			if tt.wantError {
				if err == nil {
					_ = conn.Close()

					t.Fatal("handshake succeeded")
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			defer func() {
				_ = conn.Close()
			}()

			tlsConn, ok := conn.(*tls.Conn)
			if !ok {
				t.Fatalf("dialed a %T", conn)
			}

			if v := tlsConn.ConnectionState().Version; v != tt.wantVersion {
				t.Errorf("negotiated %s", tls.VersionName(v))
			}
		})
	}
}

func serveTLS(t *testing.T, config *tls.Config) string {
	t.Helper()

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}),
		TLSConfig:         config,
		ReadHeaderTimeout: time.Second,
	}

	go func() {
		_ = server.ServeTLS(listener, "", "")
	}()

	t.Cleanup(func() {
		_ = server.Close()
	})

	return listener.Addr().String()
}

func writeKeyPair(t *testing.T, certificatePath, keyPath, hostname string, now time.Time) {
	t.Helper()

	directory, err := OpenDirectory(t.Context(), t.TempDir(), []string{
		hostname,
	}, func() time.Time {
		return now
	})
	if err != nil {
		t.Fatal(err)
	}

	for source, target := range map[string]string{
		CertificateFile: certificatePath,
		KeyFile:         keyPath,
	} {
		contents, err := os.ReadFile(filepath.Join(directory.path, source))
		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(target, contents, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.Chtimes(target, now, now); err != nil {
			t.Fatal(err)
		}
	}
}
