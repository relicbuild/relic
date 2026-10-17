package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"path"
	"regexp"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/objectstore"
	"relic/internal/platform/telemetry"
)

// StorageCheck configures CheckStorage.
type StorageCheck struct {
	// Grace protects younger objects: an upload may not have committed the
	// blob row that names them yet.
	Grace time.Duration
	// VerifyBytes is how much stored content to re-hash, the blobs verified
	// longest ago first.
	VerifyBytes int64
}

// StorageReport is what a storage check found.
type StorageReport struct {
	Objects       int              `json:"objects"`
	Adopted       int              `json:"adopted"`
	Missing       int              `json:"missing"`
	WrongSize     int              `json:"wrongSize"`
	Corrupt       int              `json:"corrupt"`
	Verified      int              `json:"verified"`
	VerifiedBytes int64            `json:"verifiedBytes"`
	Problems      []StorageProblem `json:"problems,omitempty"`
}

// StorageProblem is one blob a storage check found wrong.
type StorageProblem struct {
	SHA256  string `json:"sha256"`
	Problem string `json:"problem"`
}

const (
	storageCheckPage      = 1000
	storageReportProblems = 100
	problemMissing        = "missing"
	problemWrongSize      = "wrong size"
	problemCorrupt        = "corrupt"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// CheckStorage compares the object store with the blob rows. Objects no row
// names, older than the grace period, are adopted as unreferenced blobs so
// garbage collection removes them; referenced blobs whose object is missing
// or of the wrong size are reported; then a share of the stored content is
// re-hashed. A listing error aborts the check rather than reading as empty.
func (s *Service) CheckStorage(ctx context.Context, check StorageCheck) (report StorageReport, err error) {
	ctx, done := telemetry.Operation(ctx, "repo.CheckStorage", telemetry.StorageCheckRun)
	defer func() {
		done(err)
	}()

	if err := s.compareStorage(ctx, check.Grace, &report); err != nil {
		return report, err
	}

	if err := s.verifyStorage(ctx, check.VerifyBytes, storageCheckPage, &report); err != nil {
		return report, err
	}

	return report, nil
}

func (r *StorageReport) problem(ctx context.Context, s *Service, digest, problem, finding string) {
	telemetry.StorageCheckFinding(finding)
	s.log.ErrorContext(ctx, "storage check: "+problem, "sha256", digest)

	if len(r.Problems) < storageReportProblems {
		r.Problems = append(r.Problems, StorageProblem{
			SHA256:  digest,
			Problem: problem,
		})
	}
}

func (s *Service) compareStorage(ctx context.Context, grace time.Duration, report *StorageReport) error {
	next, stop := iter.Pull2(s.blobs.List(ctx, "sha256/"))
	defer stop()

	var (
		obj     objectstore.Object
		listErr error
		listed  bool
	)

	advance := func() {
		for {
			obj, listErr, listed = next()
			if !listed || listErr != nil || digestPattern.MatchString(path.Base(obj.Key)) {
				return
			}
		}
	}

	orphan := func(o objectstore.Object) error {
		report.Objects++

		if s.now().Sub(o.Modified) < grace {
			return nil
		}

		n, err := s.db.AdoptOrphanBlob(ctx, database.AdoptOrphanBlobParams{
			Sha256: path.Base(o.Key),
			Size:   o.Size,
		})
		if err != nil {
			return fmt.Errorf("adopt %s: %w", o.Key, err)
		}

		if n > 0 {
			report.Adopted++

			telemetry.StorageCheckFinding(telemetry.FindingAdopted)
		}

		return nil
	}

	advance()

	after := ""

	for {
		rows, err := s.db.ListBlobsAfter(ctx, database.ListBlobsAfterParams{
			After: after,
			Max:   storageCheckPage,
		})
		if err != nil {
			return fmt.Errorf("list blobs: %w", err)
		}

		for _, row := range rows {
			for listed && listErr == nil && path.Base(obj.Key) < row.Sha256 {
				if err := orphan(obj); err != nil {
					return err
				}

				advance()
			}

			if listErr != nil {
				return fmt.Errorf("list objects: %w", listErr)
			}

			switch {
			case listed && path.Base(obj.Key) == row.Sha256:
				report.Objects++

				if row.Refcount > 0 && obj.Size != row.Size {
					report.WrongSize++
					report.problem(ctx, s, row.Sha256, problemWrongSize, telemetry.FindingWrongSize)
					telemetry.BlobIntegrityFailure(telemetry.BlobCorrupt)
				}

				advance()
			case row.Refcount > 0:
				report.Missing++
				report.problem(ctx, s, row.Sha256, problemMissing, telemetry.FindingMissing)
				telemetry.BlobIntegrityFailure(telemetry.BlobMissing)
			}
		}

		if len(rows) < storageCheckPage {
			break
		}

		after = rows[len(rows)-1].Sha256
	}

	for listed && listErr == nil {
		if err := orphan(obj); err != nil {
			return err
		}

		advance()
	}

	if listErr != nil {
		return fmt.Errorf("list objects: %w", listErr)
	}

	return nil
}

func (s *Service) verifyStorage(ctx context.Context, budget int64, page int32, report *StorageReport) error {
	seen := map[string]bool{}

	for report.VerifiedBytes < budget {
		rows, err := s.db.ListBlobsToVerify(ctx, int32(len(seen))+page) //nolint:gosec // one entry per blob row
		if err != nil {
			return fmt.Errorf("list blobs to verify: %w", err)
		}

		progressed := false

		for _, row := range rows {
			if report.VerifiedBytes >= budget {
				break
			}

			if seen[row.Sha256] {
				continue
			}

			seen[row.Sha256] = true
			progressed = true

			n, err := s.rehash(ctx, row.Sha256, row.Size)

			switch {
			case errors.Is(err, objectstore.ErrNotFound):
				continue
			case errors.Is(err, ErrContentCorrupt):
				report.Corrupt++
				report.problem(ctx, s, row.Sha256, problemCorrupt, telemetry.FindingCorrupt)
				telemetry.BlobIntegrityFailure(telemetry.BlobCorrupt)
			case err != nil:
				return err
			default:
				report.Verified++

				now := s.now()
				if err := s.db.MarkBlobVerified(ctx, database.MarkBlobVerifiedParams{
					Sha256:     row.Sha256,
					VerifiedAt: &now,
				}); err != nil {
					return fmt.Errorf("mark %s verified: %w", row.Sha256, err)
				}
			}

			report.VerifiedBytes += n
			telemetry.StorageCheckVerified(n)
		}

		if !progressed {
			return nil
		}
	}

	return nil
}

func (s *Service) rehash(ctx context.Context, digest string, size int64) (int64, error) {
	body, _, err := s.blobs.Get(ctx, objectstore.Key(digest), nil)
	if err != nil {
		return 0, fmt.Errorf("verify %s: %w", digest, err)
	}

	defer func() {
		_ = body.Close()
	}()

	h := sha256.New()

	n, err := io.Copy(h, body)
	if err != nil {
		return n, fmt.Errorf("verify %s: %w", digest, err)
	}

	if n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return n, fmt.Errorf("verify %s: %w", digest, ErrContentCorrupt)
	}

	return n, nil
}
