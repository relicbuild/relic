// Package ldaptest runs an OpenLDAP server for integration tests.
package ldaptest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Image is the OpenLDAP image the tests run.
const Image = "osixia/openldap:1.5.0"

// Directory facts the seeded server has.
const (
	BaseDN        = "dc=example,dc=org"
	AdminDN       = "cn=admin," + BaseDN
	AdminPassword = "admin-pass"
)

// Person is a seeded user: uid=<UID>,ou=people.
type Person struct {
	UID, Password, Mail string
}

// Group is a seeded groupOfUniqueNames: cn=<CN>,ou=groups.
type Group struct {
	CN      string
	Members []string
}

// Server is a running, seeded OpenLDAP.
type Server struct {
	LDAP  string
	LDAPS string
	// TLS trusts the server's CA.
	TLS *tls.Config

	container testcontainers.Container
}

// PersonDN is the DN of a seeded user.
func PersonDN(uid string) string { return "uid=" + uid + ",ou=people," + BaseDN }

// GroupDN is the DN of a seeded group.
func GroupDN(cn string) string { return "cn=" + cn + ",ou=groups," + BaseDN }

// Start runs a server seeded with people and groups; it stops with the test.
func Start(t *testing.T, people []Person, groups []Group) *Server {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()

	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		t.Fatal(err)
	}

	daemonHost, err := provider.DaemonHost(ctx)
	_ = provider.Close()

	if err != nil {
		t.Fatal(err)
	}

	caPEM := writeCerts(t, dir, daemonHost)

	files := make([]testcontainers.ContainerFile, 0, 3)
	for _, f := range []string{
		"ca.crt",
		"ldap.crt",
		"ldap.key",
	} {
		files = append(files, testcontainers.ContainerFile{
			HostFilePath:      filepath.Join(dir, f),
			ContainerFilePath: "/container/service/slapd/assets/certs/" + f,
			FileMode:          0o644,
		})
	}

	c := startContainer(t, files)

	t.Cleanup(func() {
		_ = testcontainers.TerminateContainer(c)
	})

	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if host == "localhost" {
		host = "127.0.0.1"
	}

	p389, err := c.MappedPort(ctx, "389/tcp")
	if err != nil {
		t.Fatal(err)
	}

	p636, err := c.MappedPort(ctx, "636/tcp")
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	s := &Server{
		LDAP:  "ldap://" + net.JoinHostPort(host, p389.Port()),
		LDAPS: "ldaps://" + net.JoinHostPort(host, p636.Port()),
		TLS: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		},
		container: c,
	}
	s.seed(t, people, groups)

	return s
}

// Stop stops the server, to simulate a directory outage.
func (s *Server) Stop(t *testing.T) {
	t.Helper()

	timeout := 10 * time.Second
	if err := s.container.Stop(context.WithoutCancel(t.Context()), &timeout); err != nil {
		t.Fatal(err)
	}
}

// Admin returns a connection bound as the directory administrator.
func (s *Server) Admin(t *testing.T) *goldap.Conn {
	t.Helper()

	var (
		conn *goldap.Conn
		err  error
	)
	for range 60 {
		conn, err = goldap.DialURL(s.LDAP)
		if err == nil {
			if err = conn.Bind(AdminDN, AdminPassword); err == nil {
				t.Cleanup(func() {
					_ = conn.Close()
				})

				return conn
			}

			_ = conn.Close()
		}

		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("bind as the LDAP admin: %v", err)

	return nil
}

func (s *Server) seed(t *testing.T, people []Person, groups []Group) {
	t.Helper()
	conn := s.Admin(t)

	for _, ou := range []string{
		"people",
		"groups",
	} {
		add := goldap.NewAddRequest("ou="+ou+","+BaseDN, nil)
		add.Attribute("objectClass", []string{
			"organizationalUnit",
		})
		add.Attribute("ou", []string{
			ou,
		})

		if err := conn.Add(add); err != nil {
			t.Fatalf("add ou=%s: %v", ou, err)
		}
	}

	for _, p := range people {
		add := goldap.NewAddRequest(PersonDN(p.UID), nil)
		add.Attribute("objectClass", []string{
			"inetOrgPerson",
		})
		add.Attribute("uid", []string{
			p.UID,
		})
		add.Attribute("cn", []string{
			p.UID,
		})
		add.Attribute("sn", []string{
			p.UID,
		})
		add.Attribute("userPassword", []string{
			p.Password,
		})

		if p.Mail != "" {
			add.Attribute("mail", []string{
				p.Mail,
			})
		}

		if err := conn.Add(add); err != nil {
			t.Fatalf("add %s: %v", p.UID, err)
		}
	}

	for _, g := range groups {
		s.SetGroup(t, g)
	}
}

// SetGroup creates or replaces a group's members.
func (s *Server) SetGroup(t *testing.T, g Group) {
	t.Helper()
	conn := s.Admin(t)

	members := make([]string, 0, len(g.Members))
	for _, uid := range g.Members {
		members = append(members, PersonDN(uid))
	}

	if len(members) == 0 {
		members = []string{
			AdminDN,
		}
	}

	_ = conn.Del(goldap.NewDelRequest(GroupDN(g.CN), nil))
	add := goldap.NewAddRequest(GroupDN(g.CN), nil)
	add.Attribute("objectClass", []string{
		"groupOfUniqueNames",
	})
	add.Attribute("cn", []string{
		g.CN,
	})
	add.Attribute("uniqueMember", members)

	if err := conn.Add(add); err != nil {
		t.Fatalf("add group %s: %v", g.CN, err)
	}
}

func writeCerts(t *testing.T, dir, host string) []byte {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "Relic test LDAP CA",
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName: "localhost",
		},
		NotBefore: now.Add(-time.Hour),
		NotAfter:  now.Add(24 * time.Hour),
		DNSNames: []string{
			"localhost",
		},
		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
		},
		KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	} else if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	caPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caDER,
	})
	for name, data := range map[string][]byte{
		"ca.crt": caPEM,
		"ldap.crt": pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: der,
		}),
		"ldap.key": pem.EncodeToMemory(&pem.Block{
			Type:  "EC PRIVATE KEY",
			Bytes: keyDER,
		}),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil { //nolint:gosec // test certificates
			t.Fatal(fmt.Errorf("write %s: %w", name, err))
		}
	}

	return caPEM
}

// startAttempts bounds how often a container that fails to start is replaced.
const startAttempts = 3

func startContainer(t *testing.T, files []testcontainers.ContainerFile) testcontainers.Container {
	t.Helper()

	for attempt := 1; ; attempt++ {
		c, err := testcontainers.Run(t.Context(), Image,
			testcontainers.WithEnv(map[string]string{
				"LDAP_ORGANISATION":        "Relic tests",
				"LDAP_DOMAIN":              "example.org",
				"LDAP_ADMIN_PASSWORD":      AdminPassword,
				"LDAP_TLS_CRT_FILENAME":    "ldap.crt",
				"LDAP_TLS_KEY_FILENAME":    "ldap.key",
				"LDAP_TLS_CA_CRT_FILENAME": "ca.crt",
				"LDAP_TLS_VERIFY_CLIENT":   "never",
			}),
			testcontainers.WithCmd("--copy-service"),
			testcontainers.WithFiles(files...),
			testcontainers.WithExposedPorts("389/tcp", "636/tcp"),
			testcontainers.WithWaitStrategy(wait.ForAll(
				wait.ForListeningPort("389/tcp"), wait.ForListeningPort("636/tcp"),
			).WithDeadline(3*time.Minute)),
		)
		if err == nil {
			return c
		}

		logged := containerLog(c)
		_ = testcontainers.TerminateContainer(c)

		if attempt == startAttempts {
			t.Fatalf("start %s: %v\n%s", Image, err, logged)
		}

		t.Logf("start %s, attempt %d: %v\n%s", Image, attempt, err, logged)
	}
}

// containerLog is the end of what c printed, or why it cannot be read.
func containerLog(c testcontainers.Container) string {
	if c == nil {
		return "no container"
	}

	r, err := c.Logs(context.Background())
	if err != nil {
		return "container log: " + err.Error()
	}
	defer func() {
		_ = r.Close()
	}()

	b, err := io.ReadAll(r)
	if err != nil {
		return "container log: " + err.Error()
	}

	const tail = 8 << 10
	if len(b) > tail {
		b = b[len(b)-tail:]
	}

	return string(b)
}
