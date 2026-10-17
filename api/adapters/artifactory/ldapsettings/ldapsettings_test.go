package ldapsettings

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"relic/internal/access/ldap"
)

const (
	settingsDoc = `{"key":"corp","enabled":true,"ldap_url":"ldaps://ldap.example.com:636/dc=example,dc=com","user_dn_pattern":"",
		"search":{"search_filter":"(uid={0})","search_base":"ou=people","search_sub_tree":true,"manager_dn":"cn=admin,dc=example,dc=com","manager_password":"pw"},
		"auto_create_user":true,"email_attribute":"mail","ldap_position_protection":false,"allow_user_to_access_profile":true,
		"paging_support_enabled":true,"use_start_tls":false}`
	groupDoc = `{"name":"corp-groups","enabled_ldap":"corp","group_base_dn":"ou=groups","group_name_attribute":"cn",
		"group_member_attribute":"uniqueMember","sub_tree":true,"force_attribute_search":false,"filter":"(objectClass=groupOfUniqueNames)",
		"description_attribute":"description","strategy":"static"}`
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	var s Settings
	if err := json.Unmarshal([]byte(settingsDoc), &s); err != nil {
		t.Fatal(err)
	}

	stored, err := json.Marshal(FromArtifactory(s))
	if err != nil || strings.Contains(string(stored), `"ldap_url"`) ||
		!strings.Contains(string(stored), `"url":"ldaps://ldap.example.com:636/dc=example,dc=com"`) {
		t.Fatalf("stored = %s (%v), want Relic's keys", stored, err)
	}

	checkSame(t, ToArtifactory(FromArtifactory(s)), settingsDoc)

	var g GroupSettings
	if err := json.Unmarshal([]byte(groupDoc), &g); err != nil {
		t.Fatal(err)
	}

	checkSame(t, GroupToArtifactory(GroupFromArtifactory(g)), groupDoc)
}

func checkSame(t *testing.T, got any, want string) {
	t.Helper()

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	var g, w any
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(g, w) {
		t.Errorf("got  %s\nwant %s", b, want)
	}
}

func TestStrategies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		sent     string
		want     ldap.GroupStrategy
		rendered string
	}{
		{
			sent:     "STATIC",
			want:     ldap.StrategyStatic,
			rendered: "static",
		},
		{
			sent:     "dynamic",
			want:     ldap.StrategyDynamic,
			rendered: "dynamic",
		},
		{
			sent:     "HIERARCHICAL",
			want:     ldap.StrategyHierarchy,
			rendered: "hierarchical",
		},
	}
	for _, tt := range tests {
		t.Run(tt.sent, func(t *testing.T) {
			t.Parallel()

			m := GroupFromArtifactory(GroupSettings{
				Name:     "g",
				Strategy: tt.sent,
			})
			if m.Strategy != tt.want || GroupToArtifactory(m).Strategy != tt.rendered {
				t.Errorf("%q reads as %q and renders as %q", tt.sent, m.Strategy, GroupToArtifactory(m).Strategy)
			}
		})
	}
}

func TestExtension(t *testing.T) {
	t.Parallel()

	d := FromArtifactory(Settings{
		Key:                      "corp",
		AllowUserToAccessProfile: true,
	})

	const want = `{"ldap_position_protection":false,"allow_user_to_access_profile":true,"paging_support_enabled":false}`
	if string(d.Extensions[Extension]) != want {
		t.Errorf("extension = %s", d.Extensions[Extension])
	}

	if len(FromArtifactory(Settings{
		Key: "corp",
	}).Extensions) != 0 {
		t.Error("an extension without settings")
	}
}
