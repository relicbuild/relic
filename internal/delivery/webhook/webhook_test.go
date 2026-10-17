package webhook

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsValidUTF8(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("a", maxErrorLength-1) + "é and more"

	got := truncate(text)
	if !utf8.ValidString(got) || len(got) > maxErrorLength || got != strings.Repeat("a", maxErrorLength-1) {
		t.Errorf(
			"truncate of an error cut inside é = %q (%d bytes), want the text before it",
			got[len(got)-3:],
			len(got),
		)
	}
}
