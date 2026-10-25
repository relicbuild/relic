package packages

import (
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
)

func TestClientEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		format              repository.Format
		wantPackage, wantCR string
	}{
		{
			repository.FormatNpm,
			"https://r.example/npm/libs/",
			"",
		},
		{
			repository.FormatMaven,
			"https://r.example/maven/libs",
			"",
		},
		{
			repository.FormatPyPI,
			"https://r.example/pypi/libs/simple",
			"",
		},
		{
			repository.FormatGoModules,
			"https://r.example/go/libs",
			"",
		},
		{
			repository.FormatHelm,
			"https://r.example/helm/libs",
			"",
		},
		{
			repository.FormatDocker,
			"",
			"r.example/libs",
		},
		{
			repository.FormatConan,
			"https://r.example/conan/libs",
			"",
		},
		{
			repository.FormatTerraform,
			"https://r.example/terraform/libs/providers/",
			"",
		},
		{
			repository.FormatNix,
			"https://r.example/nix/libs",
			"",
		},
		{
			repository.FormatHuggingFace,
			"https://r.example/huggingface/libs",
			"",
		},
		{
			repository.FormatGitLFS,
			"https://r.example/lfs/libs",
			"",
		},
		{
			repository.FormatDebian,
			"https://r.example/debian/libs",
			"",
		},
		{
			repository.FormatRPM,
			"https://r.example/rpm/libs",
			"",
		},
		{
			repository.FormatAlpine,
			"https://r.example/alpine/libs",
			"",
		},
		{
			repository.FormatGradle,
			"https://r.example/gradle/libs",
			"",
		},
		{
			repository.FormatNuGet,
			"https://r.example/nuget/libs/index.json",
			"",
		},
		{
			repository.FormatCargo,
			"https://r.example/cargo/libs/index/",
			"",
		},
		{
			repository.FormatRubyGems,
			"https://r.example/gems/libs/",
			"",
		},
		{
			repository.FormatHelmOCI,
			"",
			"r.example/libs",
		},
		{
			repository.FormatOCI,
			"",
			"r.example/libs",
		},
		{
			repository.FormatGeneric,
			"",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			t.Parallel()

			pm, cr := ClientEndpoints("https://r.example", repository.Repository{
				Name:   "libs",
				Format: tt.format,
			})
			if pm != tt.wantPackage || cr != tt.wantCR {
				t.Errorf("ClientEndpoints = %q, %q; want %q, %q", pm, cr, tt.wantPackage, tt.wantCR)
			}

			wantPlain := strings.Replace(tt.wantPackage, "https://", "http://", 1)

			pm, cr = ClientEndpoints("http://r.example", repository.Repository{
				Name:   "libs",
				Format: tt.format,
			})
			if pm != wantPlain || cr != tt.wantCR {
				t.Errorf("ClientEndpoints over http = %q, %q; want %q, %q", pm, cr, wantPlain, tt.wantCR)
			}
		})
	}
}
