//go:build integration

package ldap_test

import (
	"crypto/tls"
	"errors"
	"slices"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	"relic/internal/access/ldap"
	"relic/internal/access/ldap/ldaptest"
	"relic/internal/platform/telemetry/telemetrytest"
)

func TestDirectory(t *testing.T) {
	t.Parallel()
	srv := ldaptest.Start(
		t,
		[]ldaptest.Person{
			{
				UID:      "alice",
				Password: "alice-pass",
				Mail:     "alice@example.org",
			},
			{
				UID:      "bob",
				Password: "bob-pass",
			},
		},
		[]ldaptest.Group{
			{
				CN: "devs",
				Members: []string{
					"alice",
				},
			},
			{
				CN: "ops",
				Members: []string{
					"alice",
					"bob",
				},
			},
		},
	)
	groups := []ldap.GroupMapping{
		{
			Name:            "corp-groups",
			Server:          "corp",
			BaseDN:          "ou=groups",
			NameAttribute:   "cn",
			MemberAttribute: "uniqueMember",
			Filter:          "(objectClass=groupOfUniqueNames)",
			Strategy:        ldap.StrategyStatic,
			SubTree:         true,
		},
	}
	search := ldap.Server{
		Name:           "corp",
		Enabled:        true,
		URL:            srv.LDAP + "/" + ldaptest.BaseDN,
		StartTLS:       true,
		EmailAttribute: "mail",
		Search: ldap.Search{
			Filter:          "(uid={0})",
			Base:            "ou=people",
			SubTree:         true,
			ManagerDN:       ldaptest.AdminDN,
			ManagerPassword: ldaptest.AdminPassword,
		},
	}

	admin := srv.Admin(t)

	contractors := goldap.NewAddRequest("ou=contractors,ou=people,"+ldaptest.BaseDN, nil)
	contractors.Attribute("objectClass", []string{
		"organizationalUnit",
	})
	contractors.Attribute("ou", []string{
		"contractors",
	})

	eve := goldap.NewAddRequest("uid=eve,ou=contractors,ou=people,"+ldaptest.BaseDN, nil)
	eve.Attribute("objectClass", []string{
		"inetOrgPerson",
	})
	eve.Attribute("uid", []string{
		"eve",
	})
	eve.Attribute("cn", []string{
		"eve",
	})
	eve.Attribute("sn", []string{
		"eve",
	})
	eve.Attribute("userPassword", []string{
		"eve-pass",
	})

	for _, add := range []*goldap.AddRequest{
		contractors,
		eve,
	} {
		if err := admin.Add(add); err != nil {
			t.Fatalf("add %s: %v", add.DN, err)
		}
	}

	pattern := ldap.Server{
		Name:           "corp",
		Enabled:        true,
		URL:            srv.LDAPS + "/" + ldaptest.BaseDN,
		UserDNPattern:  "uid={0},ou=people",
		EmailAttribute: "mail",
		Search: ldap.Search{
			ManagerDN:       ldaptest.AdminDN,
			ManagerPassword: ldaptest.AdminPassword,
		},
	}
	for _, st := range []ldap.Server{
		search,
		pattern,
	} {
		if err := st.Validate(); err != nil {
			t.Fatalf("validate %+v: %v", st, err)
		}
	}

	for _, tt := range []struct {
		name       string
		settings   ldap.Server
		groups     []ldap.GroupMapping
		user, pass string
		wantErr    error
		wantGroups []string
	}{
		{
			name:     "search and StartTLS",
			settings: search,
			groups:   groups,
			user:     "alice",
			pass:     "alice-pass",
			wantGroups: []string{
				"devs",
				"ops",
			},
		},
		{
			name:     "DN pattern over ldaps",
			settings: pattern,
			groups:   groups,
			user:     "bob",
			pass:     "bob-pass",
			wantGroups: []string{
				"ops",
			},
		},
		{
			name:     "hierarchy groups",
			settings: search,
			user:     "bob",
			pass:     "bob-pass",
			wantGroups: []string{
				"people",
			},
			groups: []ldap.GroupMapping{
				{
					Name:          "ous",
					Server:        "corp",
					NameAttribute: "ou",
					Strategy:      ldap.StrategyHierarchy,
				},
			},
		},
		{
			name:     "groups of another directory are ignored",
			settings: search,
			user:     "alice",
			pass:     "alice-pass",
			groups: []ldap.GroupMapping{
				{
					Name:            "x",
					Server:          "other",
					BaseDN:          "ou=groups",
					NameAttribute:   "cn",
					MemberAttribute: "uniqueMember",
				},
			},
		},
		{
			name:     "wrong password",
			settings: search,
			user:     "alice",
			pass:     "nope",
			wantErr:  ldap.ErrInvalidCredentials,
		},
		{
			name:     "wrong password with a DN pattern",
			settings: pattern,
			user:     "alice",
			pass:     "nope",
			wantErr:  ldap.ErrInvalidCredentials,
		},
		{
			name:     "unknown user",
			settings: search,
			user:     "carol",
			pass:     "carol-pass",
			wantErr:  ldap.ErrInvalidCredentials,
		},
		{
			name:     "empty password is never an anonymous bind",
			settings: pattern,
			user:     "alice",
			pass:     "",
			wantErr:  ldap.ErrInvalidCredentials,
		},
		{
			name:     "filter metacharacters are escaped",
			settings: search,
			user:     "ali*",
			pass:     "alice-pass",
			wantErr:  ldap.ErrInvalidCredentials,
		},
		{
			name:     "DN metacharacters are escaped",
			settings: pattern,
			user:     "eve,ou=contractors",
			pass:     "eve-pass",
			wantErr:  ldap.ErrInvalidCredentials,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e, err := ldap.Client{
				Server:   tt.settings,
				Mappings: tt.groups,
				TLS:      srv.TLS,
			}.Authenticate(t.Context(), tt.user, tt.pass)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			slices.Sort(e.Groups)

			if e.DN != ldaptest.PersonDN(tt.user) || !slices.Equal(e.Groups, tt.wantGroups) {
				t.Errorf("entry = %+v, want groups %v", e, tt.wantGroups)
			}

			if tt.user == "alice" && e.Email != "alice@example.org" {
				t.Errorf("email = %q", e.Email)
			}
		})
	}

	t.Run("an untrusted server certificate is refused", func(t *testing.T) {
		t.Parallel()

		_, err := ldap.Client{
			Server: search,
			TLS: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		}.Authenticate(t.Context(), "alice", "alice-pass")
		if !errors.Is(err, ldap.ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})
}

func TestDirectoryOutage(t *testing.T) {
	t.Parallel()
	srv := ldaptest.Start(t, []ldaptest.Person{
		{
			UID:      "alice",
			Password: "alice-pass",
		},
	}, nil)
	st := ldap.Server{
		Name:          "corp",
		Enabled:       true,
		URL:           srv.LDAP + "/" + ldaptest.BaseDN,
		UserDNPattern: "uid={0},ou=people",
	}
	d := ldap.Client{
		Server: st,
		TLS:    srv.TLS,
	}
	moved := func(op, result string, f func()) {
		t.Helper()

		labels := map[string]string{
			"directory_op": op,
			"result":       result,
		}
		before := telemetrytest.Value(t, "relic_ldap_request_duration_seconds", labels)

		f()

		if telemetrytest.Value(t, "relic_ldap_request_duration_seconds", labels) <= before {
			t.Errorf("relic_ldap_request_duration_seconds%v did not move", labels)
		}
	}
	moved("bind", "success", func() {
		if _, err := d.Authenticate(t.Context(), "alice", "alice-pass"); err != nil {
			t.Fatal(err)
		}
	})
	moved("bind", "failure", func() {
		if _, err := d.Authenticate(t.Context(), "alice", "wrong"); !errors.Is(err, ldap.ErrInvalidCredentials) {
			t.Fatalf("err = %v", err)
		}
	})
	srv.Stop(t)
	moved("dial", "error", func() {
		if _, err := d.Authenticate(t.Context(), "alice", "alice-pass"); !errors.Is(err, ldap.ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	})
}
