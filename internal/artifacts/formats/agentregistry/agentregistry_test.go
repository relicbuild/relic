package agentregistry

import (
	"errors"
	"testing"
)

func TestFrontMatter(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		text    string
		want    string
		wantErr error
	}{
		{
			name: "LF line endings",
			text: "---\nname: deploy\n---\nbody\n",
			want: "deploy",
		},
		{
			name: "CRLF line endings",
			text: "---\r\nname: deploy\r\n---\r\nbody\r\n",
			want: "deploy",
		},
		{
			name: "byte order mark",
			text: byteOrderMark + "---\nname: deploy\n---\n",
			want: "deploy",
		},
		{
			name:    "no front matter",
			text:    "name: deploy\n",
			wantErr: ErrInvalid,
		},
		{
			name:    "unclosed front matter",
			text:    "---\nname: deploy\n",
			wantErr: ErrInvalid,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := frontMatter(tt.text)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("frontMatter error = %v, want %v", err, tt.wantErr)
			}

			if tt.wantErr == nil && stringField(got, "name") != tt.want {
				t.Errorf("name = %q, want %q", stringField(got, "name"), tt.want)
			}
		})
	}
}
