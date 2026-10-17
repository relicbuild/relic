package evidence

import (
	"errors"
	"reflect"
	"testing"

	evd "relic/internal/delivery/evidence"
)

func TestAttachments(t *testing.T) {
	t.Parallel()

	body := []byte(
		`{"payload":"e30=","payloadType":"application/vnd.in-toto+json","signatures":[{"keyid":"","sig":"x"}],
		"attachments":[{"repository":"docs","path":"a/report.pdf","sha256":"abc"}]}`,
	)

	got, err := envelopeAttachments(body)
	if err != nil {
		t.Fatal(err)
	}

	want := []evd.AttachmentPlace{
		{
			Repository: "docs",
			Path:       "a/report.pdf",
			SHA256:     "abc",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}

	if _, err := envelopeAttachments([]byte(`[`)); !errors.Is(err, evd.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}

	if p := attachmentPath(evd.Attachment{
		Repository: "docs",
		Path:       "a/report.pdf",
	}); p != "docs/a/report.pdf" {
		t.Errorf("attachment path = %q", p)
	}

	if p := attachmentPath(evd.Attachment{
		Name: "unplaced",
	}); p != "" {
		t.Errorf("unplaced attachment path = %q", p)
	}

	if p := downloadPath(evd.Evidence{
		ItemPath: "r/f.txt/slsa-1.json",
	}); p != "relic-evidence/r/f.txt/slsa-1.json" {
		t.Errorf("download path = %q", p)
	}
}
