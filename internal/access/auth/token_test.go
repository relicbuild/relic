package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func stdDecodable(t *testing.T, payload []byte) {
	t.Helper()

	enc := base64.RawURLEncoding.EncodeToString(payload)
	if strings.ContainsAny(enc, "-_") {
		t.Fatalf("payload %q encodes to %q, which contains - or _", payload, enc)
	}

	dec, err := base64.RawStdEncoding.DecodeString(enc)
	if err != nil || string(dec) != string(payload) {
		t.Fatalf("RawStdEncoding round trip failed: %v", err)
	}
}

func TestStdSafeJSON(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"admin",
		"a?b",
		"x~y",
		"q>z",
		"???",
		"~~~",
		"a\x7fb",
		"ü?~",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(claims{
				Subject:  "relic/users/" + name,
				Scope:    "user",
				IssuedAt: 1,
			})
			if err != nil {
				t.Fatal(err)
			}

			safe := StdSafeJSON(raw)
			stdDecodable(t, safe)

			var back claims
			if err := json.Unmarshal(safe, &back); err != nil || back.Subject != "relic/users/"+name {
				t.Errorf("round trip = %q, %v", back.Subject, err)
			}
		})
	}
}

func FuzzStdSafeJSON(f *testing.F) {
	for _, s := range []string{
		"admin",
		"?",
		"~",
		">",
		"a?~>b",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		if !utf8.ValidString(name) {
			return
		}

		raw, err := json.Marshal(
			claims{
				Subject:   "relic/users/" + name,
				ID:        name,
				IssuedAt:  1700000000,
				ExpiresAt: 1700003600,
			},
		)
		if err != nil {
			return
		}

		safe := StdSafeJSON(raw)
		stdDecodable(t, safe)

		var back claims
		if err := json.Unmarshal(safe, &back); err != nil || back.ID != name {
			t.Fatalf("round trip of %q failed: %q, %v", name, back.ID, err)
		}
	})
}
