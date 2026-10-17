package terminal_test

import (
	"bytes"
	"strings"
	"testing"

	"relic/internal/platform/terminal"
)

func TestTablePrint(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := terminal.Table{
		Headers: []string{
			"KIND",
			"CREATED",
		},
		RightAligned: []int{
			1,
		},
		Rows: [][]string{
			{
				"groups",
				"7",
			},
			{
				"repositories",
				"126",
			},
		},
	}.Print(&out)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf(
			"printed %d lines, want a border, the headers, a rule, two rows and a border:\n%s",
			len(lines),
			out.String(),
		)
	}

	for row, cells := range map[int][]string{
		1: {
			"KIND",
			"CREATED",
		},
		3: {
			"groups",
			"      7 ",
		},
		4: {
			"repositories",
			"    126 ",
		},
	} {
		for _, cell := range cells {
			if !strings.Contains(lines[row], cell) {
				t.Errorf("line %d = %q, want it to hold %q", row, lines[row], cell)
			}
		}
	}

	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("a writer that is no terminal got colors: %q", out.String())
	}
}

func TestByteSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{
			name:  "bytes",
			bytes: 512,
			want:  "512 B",
		},
		{
			name:  "kibibytes",
			bytes: 1536,
			want:  "1.5 KiB",
		},
		{
			name:  "gibibytes",
			bytes: 1028286232699,
			want:  "957.7 GiB",
		},
		{
			name:  "tebibytes",
			bytes: 1649267441664,
			want:  "1.5 TiB",
		},
		{
			name:  "nothing",
			bytes: 0,
			want:  "0 B",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := terminal.ByteSize(tt.bytes); got != tt.want {
				t.Errorf("ByteSize(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}
