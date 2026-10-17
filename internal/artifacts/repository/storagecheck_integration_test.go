//go:build integration

package repository

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestCheckStorage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, blobs, memory := newFaultyService(t)

	intact := deploy(t, s, "intact.txt", "intact content")
	short := deploy(t, s, "short.txt", "content cut short")
	missing := deploy(t, s, "missing.txt", "content lost")
	flipped := deploy(t, s, "flipped.txt", "content flipped")

	if err := memory.Put(ctx, objectstore.Key(short.Checksums.SHA256), strings.NewReader("content"), 7); err != nil {
		t.Fatal(err)
	}

	if err := memory.Delete(ctx, objectstore.Key(missing.Checksums.SHA256)); err != nil {
		t.Fatal(err)
	}

	if err := memory.Put(
		ctx,
		objectstore.Key(flipped.Checksums.SHA256),
		strings.NewReader("CONTENT flipped"),
		15,
	); err != nil {
		t.Fatal(err)
	}

	oldOrphan := objectstore.Key(sha256Hex("an old orphan"))
	youngOrphan := objectstore.Key(sha256Hex("a young orphan"))

	for _, k := range []string{
		oldOrphan,
		youngOrphan,
	} {
		if err := memory.Put(ctx, k, strings.NewReader("orphan"), 6); err != nil {
			t.Fatal(err)
		}
	}

	memory.SetModified(oldOrphan, time.Now().Add(-2*time.Hour))

	report, err := s.CheckStorage(ctx, StorageCheck{
		Grace:       time.Hour,
		VerifyBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	if report.Adopted != 1 || report.Missing != 1 || report.WrongSize != 1 || report.Corrupt != 2 ||
		report.Verified != 1 {
		t.Fatalf(
			"report = %+v; want 1 adopted, 1 missing, 1 wrong size, 2 corrupt (short and flipped), 1 verified",
			report,
		)
	}

	problems := map[string]string{}
	for _, p := range report.Problems {
		problems[p.SHA256] += p.Problem + ";"
	}

	for digest, want := range map[string]string{
		missing.Checksums.SHA256: "missing;",
		short.Checksums.SHA256:   "wrong size;corrupt;",
		flipped.Checksums.SHA256: "corrupt;",
	} {
		if problems[digest] != want {
			t.Errorf("problems of %s = %q, want %q", digest[:8], problems[digest], want)
		}
	}

	if _, ok := problems[intact.Checksums.SHA256]; ok {
		t.Errorf("the intact blob was reported: %v", report.Problems)
	}

	s.now = func() time.Time {
		return time.Now().Add(2 * time.Hour)
	}

	if _, err := s.CollectGarbage(ctx, time.Hour, 500); err != nil {
		t.Fatal(err)
	}

	if _, err := memory.Head(ctx, oldOrphan); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("the adopted orphan survived garbage collection: %v", err)
	}

	if _, err := memory.Head(ctx, youngOrphan); err != nil {
		t.Errorf("the young orphan was collected: %v", err)
	}

	blobs.Inject(objectstoretest.Fault{
		Op: objectstoretest.OpList,
	})

	if _, err := s.CheckStorage(ctx, StorageCheck{}); !errors.Is(err, objectstoretest.ErrInjected) {
		t.Errorf("CheckStorage with a failing listing = %v, want the listing's error", err)
	}
}

func TestVerifyStorageGoesPastACorruptBlob(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, memory := newFaultyService(t)

	items := make([]Item, 0, 5)
	for i := range 5 {
		items = append(items, deploy(t, s, fmt.Sprintf("file-%d.txt", i), fmt.Sprintf("content %d", i)))
	}

	first := slices.MinFunc(items, func(a, b Item) int {
		return strings.Compare(a.Checksums.SHA256, b.Checksums.SHA256)
	})
	if err := memory.Put(
		ctx,
		objectstore.Key(first.Checksums.SHA256),
		strings.NewReader("CONTENT X"),
		first.Size,
	); err != nil {
		t.Fatal(err)
	}

	var report StorageReport
	if err := s.verifyStorage(ctx, 1<<20, 2, &report); err != nil {
		t.Fatal(err)
	}

	if report.Corrupt != 1 || report.Verified != 4 {
		t.Errorf(
			"verified %d and found %d corrupt with pages of 2, want 4 verified and 1 corrupt",
			report.Verified,
			report.Corrupt,
		)
	}
}

func TestDeployReusesAnAdoptedOrphan(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, memory := newFaultyService(t)

	key := objectstore.Key(sha256Hex("found again"))
	if err := memory.Put(ctx, key, strings.NewReader("found again"), 11); err != nil {
		t.Fatal(err)
	}

	memory.SetModified(key, time.Now().Add(-2*time.Hour))

	if report, err := s.CheckStorage(ctx, StorageCheck{
		Grace: time.Hour,
	}); err != nil || report.Adopted != 1 {
		t.Fatalf("CheckStorage = %+v, %v; want the orphan adopted", report, err)
	}

	item := deploy(t, s, "again.txt", "found again")

	b, err := s.db.GetBlob(ctx, item.Checksums.SHA256)
	if err != nil {
		t.Fatal(err)
	}

	if b.Refcount != 1 || b.Sha1 != item.Checksums.SHA1 || b.Md5 != item.Checksums.MD5 {
		t.Errorf("blob after the deploy = %+v, want refcount 1 and the deploy's checksums", b)
	}
}
