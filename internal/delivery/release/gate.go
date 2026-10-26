package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"time"

	"cel.dev/cel-go/cel"

	"relic/internal/artifacts/repository"
	"relic/internal/delivery/evidence"
	"relic/internal/platform/database"
	"relic/internal/platform/telemetry"
	"relic/internal/scanning/scan"
	"relic/internal/scanning/scan/policy"
)

// ErrRefused is a promotion a gate of the target environment refused.
var ErrRefused = errors.New("promotion refused")

// GatePredicateType is the predicate type of the evidence a gate decision
// records on the release version.
const GatePredicateType = "https://relic.build/promotion-gate/v1"

const (
	gateCost        = 100000
	maxManifestSize = 16 << 20
)

// Gate is a condition a release version must meet to be promoted to an
// environment.
type Gate struct {
	Name        string
	Environment string
	// Condition is a CEL expression over release, environment, evidence,
	// scan and signed.
	Condition   string
	Description string
	Created     time.Time
	Updated     time.Time
}

// GateDecision is one gate's verdict on a promotion.
type GateDecision struct {
	Gate        string `json:"gate"`
	Environment string `json:"environment"`
	Condition   string `json:"condition"`
	Allowed     bool   `json:"allowed"`
	// Error says why the condition could not be evaluated; the gate then
	// refuses.
	Error   string `json:"error,omitempty"`
	Release string `json:"release"`
	User    string `json:"user"`
}

var (
	gateEnv     *cel.Env
	gateEnvOnce sync.Once
	gateEnvErr  error
)

func gateEnvironment() (*cel.Env, error) {
	gateEnvOnce.Do(func() {
		gateEnv, gateEnvErr = cel.NewEnv(
			cel.Variable("release", cel.MapType(cel.StringType, cel.DynType)),
			cel.Variable("environment", cel.StringType),
			cel.Variable("evidence", cel.ListType(cel.MapType(cel.StringType, cel.DynType))),
			cel.Variable("scan", cel.MapType(cel.StringType, cel.IntType)),
			cel.Variable("signed", cel.BoolType),
		)
	})

	return gateEnv, gateEnvErr
}

func compileGate(expr string) (cel.Program, error) {
	env, err := gateEnvironment()
	if err != nil {
		return nil, err
	}

	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return nil, fmt.Errorf("%w: condition: %w", ErrInvalidGate, iss.Err())
	}

	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("%w: condition must be true or false, not %s", ErrInvalidGate, ast.OutputType())
	}

	prg, err := env.Program(ast, cel.CostLimit(gateCost))
	if err != nil {
		return nil, fmt.Errorf("%w: condition: %w", ErrInvalidGate, err)
	}

	return prg, nil
}

func domainGate(r database.PromotionGate) Gate {
	return Gate{
		Name:        r.Name,
		Environment: r.Environment,
		Condition:   r.Condition,
		Description: r.Description,
		Created:     r.Created,
		Updated:     r.Updated,
	}
}

// SetDecisions makes refused promotions recorded as policy decisions.
func (s *Service) SetDecisions(d *policy.Decisions) { s.decisions = d }

// SetEvidence makes promotions record gate decisions as evidence.
func (s *Service) SetEvidence(e *evidence.Service) { s.evidence = e }

// Gates lists the promotion gates.
func (s *Service) Gates(ctx context.Context) ([]Gate, error) {
	rows, err := s.db.ListPromotionGates(ctx)
	if err != nil {
		return nil, fmt.Errorf("list promotion gates: %w", err)
	}

	out := make([]Gate, 0, len(rows))
	for _, r := range rows {
		out = append(out, domainGate(r))
	}

	return out, nil
}

// Gate returns one promotion gate.
func (s *Service) Gate(ctx context.Context, name string) (Gate, error) {
	r, err := s.db.GetPromotionGate(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Gate{}, fmt.Errorf("%w: %s", ErrGateNotFound, name)
	}

	if err != nil {
		return Gate{}, fmt.Errorf("get promotion gate: %w", err)
	}

	return domainGate(r), nil
}

// PutGate creates or replaces a promotion gate.
func (s *Service) PutGate(ctx context.Context, g Gate) (Gate, error) {
	if !namePattern.MatchString(g.Name) {
		return Gate{}, fmt.Errorf("%w: name %q", ErrInvalidGate, g.Name)
	}

	if strings.TrimSpace(g.Environment) == "" {
		return Gate{}, fmt.Errorf("%w: a gate needs an environment", ErrInvalidGate)
	}

	if _, err := compileGate(g.Condition); err != nil {
		return Gate{}, err
	}

	if err := s.db.UpsertPromotionGate(ctx, database.UpsertPromotionGateParams{
		Name:        g.Name,
		Environment: g.Environment,
		Condition:   g.Condition,
		Description: g.Description,
		Now:         s.now(),
	}); err != nil {
		return Gate{}, fmt.Errorf("put promotion gate: %w", err)
	}

	return s.Gate(ctx, g.Name)
}

// DeleteGate removes a promotion gate.
func (s *Service) DeleteGate(ctx context.Context, name string) error {
	n, err := s.db.DeletePromotionGate(ctx, name)
	if err != nil {
		return fmt.Errorf("delete promotion gate: %w", err)
	}

	if n == 0 {
		return fmt.Errorf("%w: %s", ErrGateNotFound, name)
	}

	return nil
}

// checkGates evaluates the gates of environment on b, records each decision
// as evidence on b, and refuses when any gate does.
func (s *Service) checkGates(ctx context.Context, b Release, environment, user string) ([]GateDecision, error) {
	gates, err := s.Gates(ctx)
	if err != nil {
		return nil, err
	}

	var applicable []Gate

	for _, g := range gates {
		if strings.EqualFold(g.Environment, environment) {
			applicable = append(applicable, g)
		}
	}

	if len(applicable) == 0 {
		return nil, nil
	}

	vars, err := s.gateVariables(ctx, b, environment)
	if err != nil {
		return nil, err
	}

	decisions := make([]GateDecision, 0, len(applicable))

	var refused []string

	for _, g := range applicable {
		d := GateDecision{
			Gate:        g.Name,
			Environment: environment,
			Condition:   g.Condition,
			Release:     b.Folder(),
			User:        user,
		}

		d.Allowed, err = evaluateGate(g.Condition, vars)
		if err != nil {
			d.Error = err.Error()
		}

		telemetry.GateDecided(d.Allowed)

		if !d.Allowed {
			reason := g.Name
			if g.Description != "" {
				reason += " (" + g.Description + ")"
			}

			refused = append(refused, reason)

			s.decisions.Record(ctx, policy.Decision{
				Kind:    policy.DecisionRelease,
				Subject: b.Folder(),
				Policy:  g.Name,
				Rule:    environment,
				Reason:  "gate " + reason + " refused the promotion to " + environment,
				Actor:   user,
			})
		}

		s.recordDecision(ctx, b, d)
		decisions = append(decisions, d)
	}

	if len(refused) > 0 {
		return decisions, fmt.Errorf("%w: to %s by %s", ErrRefused, environment, strings.Join(refused, ", "))
	}

	return decisions, nil
}

func evaluateGate(condition string, vars map[string]any) (bool, error) {
	prg, err := compileGate(condition)
	if err != nil {
		return false, err
	}

	out, _, err := prg.Eval(vars)
	if err != nil {
		return false, err
	}

	allowed, ok := out.Value().(bool)

	return ok && allowed, nil
}

func (s *Service) recordDecision(ctx context.Context, b Release, d GateDecision) {
	if s.evidence == nil {
		return
	}

	if _, err := s.evidence.Record(
		ctx,
		b.Repository,
		s.ManifestPath(b),
		GatePredicateType,
		d,
		d.User,
	); err != nil {
		s.log.WarnContext(ctx, "record gate decision", "release", b.Folder(), "gate", d.Gate, "error", err)
	}
}

func (s *Service) gateVariables(ctx context.Context, b Release, environment string) (map[string]any, error) {
	artifacts := make([]any, 0, len(b.Artifacts))
	shas := make([]string, 0, len(b.Artifacts))

	for _, a := range b.Artifacts {
		artifacts = append(artifacts, map[string]any{
			"repository": a.Repository,
			"path":       a.Path,
			"sha256":     a.SHA256,
			"size":       a.Size,
		})
		shas = append(shas, a.SHA256)
	}

	counts, err := s.scanCounts(ctx, shas)
	if err != nil {
		return nil, err
	}

	ev, err := s.releaseEvidence(ctx, b)
	if err != nil {
		return nil, err
	}

	signed, err := s.signed(ctx, b)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"release": map[string]any{
			"project":   b.Project,
			"name":      b.Name,
			"version":   b.Version,
			"tag":       b.Tag,
			"createdBy": b.CreatedBy,
			"artifacts": artifacts,
		},
		"environment": environment,
		"evidence":    ev,
		"scan":        counts,
		"signed":      signed,
	}, nil
}

// scanCounts counts the findings of the artifacts by severity, the malicious
// ones, and the artifacts Relic Scan has not scanned.
func (s *Service) scanCounts(ctx context.Context, shas []string) (map[string]int64, error) {
	counts := map[string]int64{
		string(scan.SeverityCritical): 0,
		string(scan.SeverityHigh):     0,
		string(scan.SeverityMedium):   0,
		string(scan.SeverityLow):      0,
		string(scan.SeverityUnknown):  0,
		"malicious":                   0,
		"unscanned":                   0,
	}

	if len(shas) == 0 {
		return counts, nil
	}

	findings, err := s.db.ListFindingsForContents(ctx, shas)
	if err != nil {
		return nil, fmt.Errorf("list findings: %w", err)
	}

	for _, f := range findings {
		if _, ok := counts[f.Severity]; ok {
			counts[f.Severity]++
		} else {
			counts[string(scan.SeverityUnknown)]++
		}

		if scan.IsMalicious(f.VulnID) {
			counts["malicious"]++
		}
	}

	for _, sha := range shas {
		a, err := s.db.GetScanStatus(ctx, sha)

		switch {
		case errors.Is(database.NotFound(err), database.ErrNotFound):
			counts["unscanned"]++
		case err != nil:
			return nil, fmt.Errorf("load scan status: %w", err)
		case a.Status != scan.StatusDone && a.Status != scan.StatusPartial:
			counts["unscanned"]++
		}
	}

	return counts, nil
}

func (s *Service) releaseEvidence(ctx context.Context, b Release) ([]any, error) {
	if s.evidence == nil {
		return []any{}, nil
	}

	list, err := s.evidence.ForCurrentContent(ctx, b.Repository, b.Folder(), s.layout.ManifestName)
	if err != nil {
		return nil, err
	}

	out := make([]any, 0, len(list))

	for _, e := range list {
		if e.PredicateType == GatePredicateType {
			continue
		}

		var predicate any = map[string]any{}
		if len(e.Predicate) > 0 {
			if err := json.Unmarshal(e.Predicate, &predicate); err != nil {
				predicate = map[string]any{}
			}
		}

		out = append(out, map[string]any{
			"predicateType": e.PredicateType,
			"verified":      e.Verified,
			"keyAlias":      e.KeyAlias,
			"createdBy":     e.CreatedBy,
			"predicate":     predicate,
		})
	}

	return out, nil
}

// signed reports whether the stored manifest of b verifies with Relic's
// release key, names b's artifacts, and still matches their stored copies.
func (s *Service) signed(ctx context.Context, b Release) (bool, error) {
	item, err := s.repos.Stat(ctx, b.Repository, s.ManifestPath(b))
	if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrRepositoryNotFound) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("stat release manifest: %w", err)
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return false, fmt.Errorf("open release manifest: %w", err)
	}

	defer func() {
		_ = rc.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(rc, maxManifestSize))
	if err != nil {
		return false, fmt.Errorf("read release manifest: %w", err)
	}

	var env evidence.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return false, nil
	}

	key, _, err := s.keys.RSA(ctx, SigningKey)
	if err != nil {
		return false, fmt.Errorf("release signing key: %w", err)
	}

	if !evidence.Verify(key.Public(), env) {
		return false, nil
	}

	statement, err := parseStatement(env)
	if err != nil || statement.Predicate.Name != b.Name || statement.Predicate.Version != b.Version ||
		!maps.Equal(statement.digests(), artifactDigests(b.Artifacts)) {
		return false, nil
	}

	_, err = s.storedArtifacts(ctx, b)
	if errors.Is(err, ErrInvalid) || errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return true, nil
}
