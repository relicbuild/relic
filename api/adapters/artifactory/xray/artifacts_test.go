package xray

import (
	"encoding/json"
	"testing"
)

func TestLicenseName(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, raw, want string
	}{
		{
			"object",
			`{"value":"MIT"}`,
			"MIT",
		},
		{
			"plain string",
			`"GPL-2.0-only"`,
			"GPL-2.0-only",
		},
		{
			"unidentified text is named by its hash",
			`{"value":"sha256:fd7e4aae"}`,
			"",
		},
		{
			"hash as a plain string",
			`"sha256:ab"`,
			"",
		},
		{
			"hash as a license reference",
			`{"value":"LicenseRef-sha256-fd7e4aae"}`,
			"",
		},
		{
			"not a license",
			`42`,
			"",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := licenseName(json.RawMessage(tt.raw)); got != tt.want {
				t.Errorf("licenseName(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
