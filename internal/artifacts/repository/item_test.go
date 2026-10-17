package repository

import (
	"errors"
	"testing"
)

func TestPropertiesValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		props   Properties
		wantErr bool
	}{
		{
			name: "plain text",
			props: Properties{
				"build.name": {
					"Straße",
				},
			},
		},
		{
			name: "NUL in a value",
			props: Properties{
				"k1": {
					"a\x00b",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid UTF-8 in a value",
			props: Properties{
				"k2": {
					"a\xffb",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid UTF-8 in a key",
			props: Properties{
				"k\xff": {
					"v",
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.props.validate()
			if tt.wantErr != errors.Is(err, ErrInvalidProperty) || !tt.wantErr && err != nil {
				t.Errorf("validate() = %v, want an error: %v", err, tt.wantErr)
			}
		})
	}
}
