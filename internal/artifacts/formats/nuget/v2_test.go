package nuget

import (
	"encoding/xml"
	"testing"
)

func TestShortFramework(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, want string
	}{
		{
			"netstandard",
			".NETStandard2.0",
			"netstandard2.0",
		},
		{
			"netframework",
			".NETFramework4.6.2",
			"net462",
		},
		{
			"netcoreapp",
			".NETCoreApp3.1",
			"netcoreapp3.1",
		},
		{
			"short name",
			"net8.0",
			"net8.0",
		},
		{
			"empty",
			"",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ShortFramework(tt.in); got != tt.want {
				t.Errorf("ShortFramework(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestV2Dependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, nuspec, want string
	}{
		{
			"none",
			`<package><metadata/></package>`,
			"",
		},
		{
			"flat",
			`<package><metadata><dependencies><dependency id="A" version="1.0"/></dependencies></metadata></package>`,
			"A:1.0:",
		},
		{
			"groups",
			`<package><metadata><dependencies><group targetFramework=".NETStandard2.0"><dependency id="A" version="[1.0, )"/><dependency id="B" version="2.0"/></group><group targetFramework="net8.0"/></dependencies></metadata></package>`,
			"A:[1.0, ):netstandard2.0|B:2.0:netstandard2.0|::net8.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var n Nuspec
			if err := xml.Unmarshal([]byte(tt.nuspec), &n); err != nil {
				t.Fatal(err)
			}

			if got := v2Dependencies(n); got != tt.want {
				t.Errorf("v2Dependencies = %q, want %q", got, tt.want)
			}
		})
	}
}
