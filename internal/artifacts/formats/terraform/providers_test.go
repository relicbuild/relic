package terraform

import "testing"

func TestParseFilename(t *testing.T) {
	t.Parallel()

	a := Address{
		Hostname:  "registry.terraform.io",
		Namespace: "hashicorp",
		Type:      "random",
	}

	tests := []struct {
		name, file, version, platform string
		ok                            bool
	}{
		{
			"archive",
			"terraform-provider-random_3.6.3_linux_amd64.zip",
			"3.6.3",
			"linux_amd64",
			true,
		},
		{
			"prerelease",
			"terraform-provider-random_4.0.0-beta1_darwin_arm64.zip",
			"4.0.0-beta1",
			"darwin_arm64",
			true,
		},
		{
			"another type",
			"terraform-provider-null_3.2.3_linux_amd64.zip",
			"",
			"",
			false,
		},
		{
			"no platform",
			"terraform-provider-random_3.6.3.zip",
			"",
			"",
			false,
		},
		{
			"not a zip",
			"terraform-provider-random_3.6.3_linux_amd64.json",
			"",
			"",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			v, p, ok := a.parseFilename(tt.file)
			if v != tt.version || p != tt.platform || ok != tt.ok {
				t.Errorf("parseFilename(%q) = %q, %q, %v", tt.file, v, p, ok)
			}
		})
	}

	if _, ok := ParseAddress("registry.terraform.io", "../etc", "x"); ok {
		t.Error("a traversal namespace was accepted")
	}
}
