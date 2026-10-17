package relicapi

import (
	"testing"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/artifacts/repository"
)

func TestFormatsMatchEnum(t *testing.T) {
	t.Parallel()

	for _, f := range repository.Formats {
		m := formatMessage(f)
		if m == relicv1.Format_FORMAT_UNSPECIFIED {
			t.Errorf("format %q has no enum value", f)
			continue
		}

		if back, err := formatOf(m); err != nil || back != f {
			t.Errorf("formatOf(%v) = %q, %v; want %q", m, back, err, f)
		}
	}

	if len(formats) != len(repository.Formats) {
		t.Errorf("enum has %d formats, repo.Formats %d", len(formats), len(repository.Formats))
	}
}
