package blocking

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"relic/internal/platform/database"
	"relic/internal/platform/events"
	"relic/internal/scanning/scan"
)

// QuarantineState is why a quarantined remote repository holds a file.
type QuarantineState string

// Quarantine states.
const (
	// AwaitingScan: Relic Scan has not scanned the file yet.
	AwaitingScan QuarantineState = "awaiting_scan"
	// ScanFailed: the scan failed; an administrator decides.
	ScanFailed QuarantineState = "scan_failed"
	// ScanSkipped: Relic Scan skipped the file; an administrator decides.
	ScanSkipped QuarantineState = "scan_skipped"
	// Malicious: the file is a known malicious package.
	Malicious QuarantineState = "malicious"
	// Rejected: an administrator rejected the file.
	Rejected QuarantineState = "rejected"
)

// Errors.
var (
	ErrNotQuarantined = errors.New("the file is not in a quarantined repository")
	ErrInvalid        = errors.New("invalid quarantine decision")
)

// QuarantinedRepositories tells which remote repositories quarantine what
// they cache.
type QuarantinedRepositories interface {
	QuarantinedRepositories(ctx context.Context) ([]string, error)
	Quarantines(ctx context.Context, repoKey string) (bool, error)
}

// SetQuarantine makes the blocker hold files the repositories qr names cache,
// until they are scanned and not malicious, or an administrator releases
// them.
func (b *Blocker) SetQuarantine(qr QuarantinedRepositories) { b.quarantined = qr }

// Held is a file a quarantined repository holds.
type Held struct {
	Repository string
	Path       string
	SHA256     string
	Cached     time.Time
	State      QuarantineState
	// Reason explains a rejection, as the administrator gave it.
	Reason    string
	DecidedBy string
}

func (b *Blocker) quarantines(ctx context.Context, repoKey string) (bool, error) {
	if b.quarantined == nil {
		return false, nil
	}

	return b.quarantined.Quarantines(ctx, repoKey)
}

// holdState is why the quarantine of repoKey holds a file, or "".
func holdState(scanStatus string, malicious bool, decision string) QuarantineState {
	switch {
	case decision == "released":
		return ""
	case decision == "rejected":
		return Rejected
	case scanStatus == "" || scanStatus == scan.StatusQueued:
		return AwaitingScan
	case scanStatus == scan.StatusFailed:
		return ScanFailed
	case scanStatus == scan.StatusSkipped:
		return ScanSkipped
	case malicious:
		return Malicious
	}

	return ""
}

var holdReasons = map[QuarantineState]string{
	AwaitingScan: "quarantined until Relic Scan has scanned it",
	ScanFailed:   "quarantined: its scan failed; an administrator can release it",
	ScanSkipped:  "quarantined: Relic Scan skipped it; an administrator can release it",
	Malicious:    "quarantined: it is a known malicious package",
	Rejected:     "rejected from quarantine by an administrator",
}

// quarantine decides for a file of a quarantined repository.
func (b *Blocker) quarantine(ctx context.Context, repoKey, itemPath, sha256Hex string) (Decision, error) {
	held, err := b.quarantines(ctx, repoKey)
	if err != nil || !held || !scannable.MatchString(path.Base(itemPath)) {
		return Decision{}, err
	}

	decision := ""

	d, err := b.db.GetQuarantineDecision(ctx, database.GetQuarantineDecisionParams{
		RepoKey: repoKey,
		Sha256:  sha256Hex,
	})

	switch {
	case err == nil:
		decision = d.Decision
	case !errors.Is(database.NotFound(err), database.ErrNotFound):
		return Decision{}, fmt.Errorf("load quarantine decision: %w", err)
	}

	status := ""

	a, err := b.db.GetScanArtifact(ctx, sha256Hex)

	switch {
	case err == nil:
		status = a.Status
	case !errors.Is(database.NotFound(err), database.ErrNotFound):
		return Decision{}, fmt.Errorf("load scan status: %w", err)
	}

	malicious := false

	if status == scan.StatusDone || status == scan.StatusPartial {
		findings, err := b.db.ListFindings(ctx, sha256Hex)
		if err != nil {
			return Decision{}, fmt.Errorf("load findings: %w", err)
		}

		for _, f := range findings {
			malicious = malicious || scan.IsMalicious(f.VulnID)
		}
	}

	state := holdState(status, malicious, decision)
	if state == "" {
		return Decision{}, nil
	}

	reason := holdReasons[state]
	if state == Rejected && d.Reason != "" {
		reason += ": " + d.Reason
	}

	return Decision{
		Blocked: true,
		Reason:  reason,
		Policy:  "quarantine",
		Rule:    string(state),
	}, nil
}

const (
	quarantinePage    = 1000
	maxQuarantineList = 10_000
	maxQuarantineScan = 1_000_000
)

// Quarantined lists the files quarantined repositories hold, newest first.
func (b *Blocker) Quarantined(ctx context.Context, limit int) ([]Held, error) {
	if b.quarantined == nil {
		return nil, nil
	}

	keys, err := b.quarantined.QuarantinedRepositories(ctx)
	if err != nil || len(keys) == 0 {
		return nil, err
	}

	limit = min(limit, maxQuarantineList)

	var out []Held

	for skip := 0; len(out) < limit; skip += quarantinePage {
		rows, err := b.db.ListQuarantineCandidates(ctx, database.ListQuarantineCandidatesParams{
			RepoKeys:    keys,
			NamePattern: scan.ScannablePattern,
			MaxRows:     quarantinePage,
			Skip:        int32(skip), //nolint:gosec // at most maxQuarantineScan
		})
		if err != nil {
			return nil, fmt.Errorf("list quarantine: %w", err)
		}

		for _, r := range rows {
			if r.Sha256 == nil || len(out) == limit {
				continue
			}

			state := holdState(r.ScanStatus, r.Malicious, r.Decision)
			if state == "" {
				continue
			}

			out = append(out, Held{
				Repository: r.RepoKey,
				Path:       strings.TrimPrefix(path.Join(r.Path, r.Name), "/"),
				SHA256:     *r.Sha256,
				Cached:     r.Created,
				State:      state,
				Reason:     r.DecisionReason,
				DecidedBy:  r.DecidedBy,
			})
		}

		if len(rows) < quarantinePage || skip >= maxQuarantineScan {
			break
		}
	}

	return out, nil
}

// Decide releases (serves) or rejects (never serves) a file of a
// quarantined repository, whatever its scan says.
func (b *Blocker) Decide(ctx context.Context, repoKey, sha256Hex string, release bool, reason, user string) error {
	held, err := b.quarantines(ctx, repoKey)
	if err != nil {
		return err
	}

	if !held {
		return fmt.Errorf("%w: %s", ErrNotQuarantined, repoKey)
	}

	if len(sha256Hex) != 64 {
		return fmt.Errorf("%w: the file has no content digest", ErrInvalid)
	}

	decision := "rejected"
	if release {
		decision = "released"
	}

	if err := b.db.PutQuarantineDecision(ctx, database.PutQuarantineDecisionParams{
		RepoKey:   repoKey,
		Sha256:    sha256Hex,
		Decision:  decision,
		Reason:    reason,
		DecidedBy: user,
		Decided:   b.now().UTC(),
	}); err != nil {
		return fmt.Errorf("record quarantine decision: %w", err)
	}

	eventType := events.QuarantineRejected
	if release {
		eventType = events.QuarantineReleased
	}

	events.Publish(ctx, b.events, events.Event{
		Type:    eventType,
		Subject: repoKey + "/sha256:" + sha256Hex,
		Actor:   user,
		Data: map[string]any{
			"repository": repoKey,
			"sha256":     sha256Hex,
			"reason":     reason,
		},
	})

	return nil
}
