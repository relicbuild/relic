//go:build integration

package database_test

import (
	"testing"
	"time"
	"uuid"

	"relic/internal/platform/database"
	"relic/internal/platform/database/databasetest"
)

func TestAuditSearchTakesWildcardsLiterally(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)
	now := time.Now()

	for _, target := range []string{
		"my_repo",
		"my-repo",
		"100%",
	} {
		if err := db.InsertAuditEvent(ctx, database.InsertAuditEventParams{
			ID:      uuid.NewV7(),
			Time:    now,
			Actor:   "admin",
			Action:  "repository.update",
			Target:  target,
			Outcome: "success",
			Detail:  "{}",
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		name   string
		target string
		want   int
	}{
		{
			name:   "an underscore is literal, in any case",
			target: "MY_REPO",
			want:   1,
		},
		{
			name:   "a LIKE wildcard is literal",
			target: "%",
			want:   1,
		},
		{
			name:   "a substring",
			target: "repo",
			want:   2,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events, err := db.ListAuditEvents(ctx, database.ListAuditEventsParams{
				Target:  tt.target,
				Since:   now.Add(-time.Hour),
				Until:   now.Add(time.Hour),
				MaxRows: 10,
			})
			if err != nil {
				t.Fatal(err)
			}

			if len(events) != tt.want {
				t.Errorf("search for %q found %d events, want %d", tt.target, len(events), tt.want)
			}
		})
	}
}

func TestSetCVEKEVWithAnEmptyListClearsTheFlags(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	db := databasetest.NewDB(t)

	if err := db.AcquireBlob(ctx, database.AcquireBlobParams{
		Sha256: "aa",
		Sha1:   "a1",
		Md5:    "a5",
		Size:   1,
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.ImportScanSbom(ctx, database.ImportScanSbomParams{
		Sha256:   "aa",
		FileName: "a.tgz",
		Sbom:     []byte("{}"),
		Now:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.InsertFinding(ctx, database.InsertFindingParams{
		Sha256:         "aa",
		VulnID:         "CVE-2026-1",
		Purl:           "pkg:npm/a@1",
		PackageName:    "a",
		PackageVersion: "1",
		Severity:       "high",
		Cves:           []string{},
		FixedVersions:  []string{},
		Cvss:           []byte("[]"),
		Urls:           []string{},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.RegisterFindingCVEs(ctx); err != nil {
		t.Fatal(err)
	}

	if err := db.SetCVEKEV(ctx, database.SetCVEKEVParams{
		Ids: []string{
			"CVE-2026-1",
		},
		Added: []time.Time{
			time.Now(),
		},
		Due: []time.Time{
			time.Now(),
		},
		Ransomware: []bool{
			true,
		},
		Actions: []string{
			"patch",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.SetCVEKEV(ctx, database.SetCVEKEVParams{}); err != nil {
		t.Fatal(err)
	}

	cve, err := db.GetCVE(ctx, "CVE-2026-1")
	if err != nil {
		t.Fatal(err)
	}

	if cve.KevAdded != nil || cve.KevDue != nil || cve.KevRansomware || cve.KevAction != "" {
		t.Errorf(
			"KEV flags = %v, %v, %v, %q after an empty KEV list, want none",
			cve.KevAdded,
			cve.KevDue,
			cve.KevRansomware,
			cve.KevAction,
		)
	}
}
