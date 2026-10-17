package repository

import (
	"errors"
	"testing"
)

func TestValidateFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		format  Format
		want    Format
		wantErr bool
	}{
		{
			name:   "known",
			format: FormatRubyGems,
			want:   FormatRubyGems,
		},
		{
			name:   "empty is generic",
			format: "",
			want:   FormatGeneric,
		},
		{
			name:    "a package type Relic renamed",
			format:  "gems",
			wantErr: true,
		},
		{
			name:    "unknown",
			format:  "yum",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := Repository{
				Name:   "r",
				Class:  ClassLocal,
				Format: tt.format,
			}

			err := r.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidRepository) {
					t.Errorf("Validate() = %v, want ErrInvalidRepo", err)
				}

				return
			}

			if err != nil || r.Format != tt.want {
				t.Errorf("Validate() = %v, format %q; want %q", err, r.Format, tt.want)
			}
		})
	}
}
