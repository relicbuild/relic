package telemetry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRecordersBoundTheirLabels(t *testing.T) {
	t.Parallel()

	const junk = "junk-/../x@y"

	ctx := context.Background()
	err := errors.New("boom")

	ObserveHTTP(ctx, HTTPRequest{
		Path:    "/" + junk,
		Pattern: "",
		Method:  junk,
		Status:  777,
	})
	AuthAttempt(junk, junk)
	TokenIssued(junk)
	StartLDAP(ctx, junk)(junk)
	ArtifactUploaded(junk, junk, junk, 1, true)
	ArtifactDownloaded(ctx, junk, junk, junk, junk, 1)
	RemoteCache(junk, junk, junk)
	CacheLookup(junk, true)
	_, end := StartObjectStorage(ctx, junk, junk)
	end(junk)
	ObjectStorageBytes(junk, 1)
	BlobIntegrityFailure(junk)
	StorageCheckFinding(junk)
	JobWorked(ctx, Other, junk, time.Second)
	_ = ScanStage(ctx, junk, func(context.Context) error {
		return err
	})
	ScanFinished(junk)
	DownloadBlocked(junk)
	DownloadNotHeld(junk)
	ReleaseOp(junk, err)
	Promoted(junk, nil)
	TLSCertificateServed(junk, time.Now())
	TLSCertificateIssued(err)
	XetUploaded(junk, 1, nil)
	PackumentCacheLookup(junk, junk)
	PackumentCacheSize(junk, 1)
	SetInventory(Inventory{
		Repositories: map[[2]string]int64{
			{
				junk,
				junk,
			}: 1,
		},
		FeedFetched: map[string]time.Time{
			junk: time.Now(),
		},
	}, time.Now())

	mfs, gerr := registry.Gather()
	if gerr != nil {
		t.Fatal(gerr)
	}

	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if strings.Contains(l.GetValue(), "junk") {
					t.Errorf("%s{%s=%q}", mf.GetName(), l.GetName(), l.GetValue())
				}
			}
		}
	}
}
