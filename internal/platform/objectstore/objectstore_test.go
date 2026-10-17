package objectstore_test

import (
	"testing"

	"relic/internal/platform/objectstore"
)

func TestKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		sha256 string
		want   string
	}{
		{
			name:   "the digest of the empty file",
			sha256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			want:   "sha256/e3/b0/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:   "a digest that starts with zeros",
			sha256: "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
			want:   "sha256/00/01/000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := objectstore.Key(tt.sha256); got != tt.want {
				t.Errorf("Key = %q, want %q: stored objects are found by this key, so it never changes", got, tt.want)
			}
		})
	}
}
