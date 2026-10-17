package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"relic/internal/platform/servertls"
)

func trustedAuthority(
	cmd *cobra.Command,
	server *url.URL,
	certificateAuthorityFile string,
	canAsk bool,
) (string, error) {
	if certificateAuthorityFile != "" {
		return readCertificateAuthority(certificateAuthorityFile)
	}

	if server.Scheme != "https" {
		return "", nil
	}

	port := server.Port()
	if port == "" {
		port = "443"
	}

	dialer := &tls.Dialer{
		Config: &tls.Config{
			ServerName: server.Hostname(),
			MinVersion: tls.VersionTLS12,
		},
	}

	conn, err := dialer.DialContext(cmd.Context(), "tcp", net.JoinHostPort(server.Hostname(), port))
	if err == nil {
		_ = conn.Close()

		return "", nil
	}

	var verification *tls.CertificateVerificationError

	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(err, &verification) || !errors.As(verification.Err, &unknownAuthority) {
		return "", fmt.Errorf("connect to %s: %w", server.Host, err)
	}

	chain := verification.UnverifiedCertificates
	authority := chain[len(chain)-1]

	if err := verifyChain(chain, authority, server.Hostname()); err != nil {
		return "", fmt.Errorf("%s's certificate does not verify against the authority it names: %w", server.Host, err)
	}

	fingerprint := servertls.Fingerprint(authority)
	if !canAsk {
		return "", fmt.Errorf(
			"%s's certificate comes from an authority this machine does not trust (%s, SHA-256 %s): "+
				"check the fingerprint with the server's administrator and pass its certificate with --ca-file",
			server.Host,
			authority.Subject.CommonName,
			fingerprint,
		)
	}

	_, _ = fmt.Fprintf(
		cmd.ErrOrStderr(),
		"%s's certificate comes from an authority this machine does not trust:\n  %s\n  SHA-256 %s\n"+
			"Check the fingerprint with the server's administrator. Trust this authority for %s? [y/N] ",
		server.Host,
		authority.Subject.CommonName,
		fingerprint,
		server.Host,
	)

	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the answer: %w", err)
	}

	if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
		return "", errors.New("the server's certificate authority is not trusted")
	}

	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: authority.Raw,
	})), nil
}

func verifyChain(chain []*x509.Certificate, authority *x509.Certificate, hostname string) error {
	roots := x509.NewCertPool()
	roots.AddCert(authority)

	intermediates := x509.NewCertPool()
	for _, certificate := range chain[1:] {
		intermediates.AddCert(certificate)
	}

	if _, err := chain[0].Verify(x509.VerifyOptions{
		DNSName:       hostname,
		Roots:         roots,
		Intermediates: intermediates,
	}); err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	return nil
}

func readCertificateAuthority(path string) (string, error) {
	authority, err := os.ReadFile(path) //nolint:gosec // a file the user names with --ca-file
	if err != nil {
		return "", fmt.Errorf("--ca-file: %w", err)
	}

	if !x509.NewCertPool().AppendCertsFromPEM(authority) {
		return "", fmt.Errorf("--ca-file %s holds no PEM certificate", path)
	}

	return string(authority), nil
}

func stdinIsTerminal(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)

	return ok && term.IsTerminal(int(f.Fd()))
}
