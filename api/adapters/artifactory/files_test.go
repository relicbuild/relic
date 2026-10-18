package artifactory

import (
	"net/http"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
)

func TestParseChecksumHeaders(t *testing.T) {
	t.Parallel()

	md5 := strings.Repeat("a", 32)
	sha1 := strings.Repeat("b", 40)
	sha256 := strings.Repeat("c", 64)

	tests := []struct {
		name    string
		headers map[string]string
		want    repository.Checksums
	}{
		{
			name: "typed headers",
			headers: map[string]string{
				"X-Checksum-Md5":    md5,
				"X-Checksum-Sha1":   sha1,
				"X-Checksum-Sha256": sha256,
			},
			want: repository.Checksums{
				MD5:    md5,
				SHA1:   sha1,
				SHA256: sha256,
			},
		},
		{
			name: "an MD5 in X-Checksum",
			headers: map[string]string{
				"X-Checksum": md5,
			},
			want: repository.Checksums{
				MD5: md5,
			},
		},
		{
			name: "a SHA-1 in X-Checksum",
			headers: map[string]string{
				"X-Checksum": sha1,
			},
			want: repository.Checksums{
				SHA1: sha1,
			},
		},
		{
			name: "a SHA-256 in X-Checksum",
			headers: map[string]string{
				"X-Checksum": sha256,
			},
			want: repository.Checksums{
				SHA256: sha256,
			},
		},
		{
			name: "a typed header wins over X-Checksum",
			headers: map[string]string{
				"X-Checksum":      strings.Repeat("d", 40),
				"X-Checksum-Sha1": sha1,
			},
			want: repository.Checksums{
				SHA1: sha1,
			},
		},
		{
			name: "a value of no known length is compared as a SHA-256",
			headers: map[string]string{
				"X-Checksum": "abc",
			},
			want: repository.Checksums{
				SHA256: "abc",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}

			if got := parseChecksumHeaders(h); got != tt.want {
				t.Errorf("parseChecksumHeaders = %+v, want %+v", got, tt.want)
			}
		})
	}
}
