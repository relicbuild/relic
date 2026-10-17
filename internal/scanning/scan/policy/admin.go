package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
	"uuid"

	"relic/internal/platform/database"
)

// Errors of policy administration.
var (
	ErrExists   = errors.New("already exists")
	ErrNotFound = errors.New("not found")
	ErrInUse    = errors.New("in use")
)

// Admin stores policies, watches and ignore rules.
type Admin struct {
	DB      *database.DB
	Changed func()
}

func (a *Admin) changed() {
	if a.Changed != nil {
		a.Changed()
	}
}

// Policies lists every policy.
func (a *Admin) Policies(ctx context.Context) ([]Policy, error) {
	rows, err := a.DB.ListScanPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}

	out := make([]Policy, 0, len(rows))
	for _, r := range rows {
		var p Policy
		if json.Unmarshal(r.Doc, &p) == nil {
			out = append(out, p)
		}
	}

	return out, nil
}

// PolicyMap returns the policies by name.
func (a *Admin) PolicyMap(ctx context.Context) (map[string]Policy, error) {
	list, err := a.Policies(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[string]Policy, len(list))
	for _, p := range list {
		out[p.Name] = p
	}

	return out, nil
}

// Policy returns one policy.
func (a *Admin) Policy(ctx context.Context, name string) (Policy, error) {
	row, err := a.DB.GetScanPolicy(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Policy{}, fmt.Errorf("policy %s: %w", name, ErrNotFound)
	}

	if err != nil {
		return Policy{}, fmt.Errorf("get policy: %w", err)
	}

	var p Policy
	if err := json.Unmarshal(row.Doc, &p); err != nil {
		return Policy{}, fmt.Errorf("decode policy %s: %w", name, err)
	}

	return p, nil
}

// SavePolicy creates (create set) or replaces an existing policy.
func (a *Admin) SavePolicy(ctx context.Context, p Policy, create bool) error {
	if err := p.Validate(); err != nil {
		return err
	}

	doc, err := json.Marshal(p)
	if err != nil {
		return err
	}

	now := time.Now().UTC()

	defer a.changed()

	if create {
		n, err := a.DB.InsertScanPolicy(
			ctx,
			database.InsertScanPolicyParams{
				Name: p.Name,
				Kind: string(p.Kind),
				Doc:  doc,
				Now:  now,
			},
		)
		if err != nil {
			return fmt.Errorf("create policy: %w", err)
		}

		if n == 0 {
			return fmt.Errorf("policy %s: %w", p.Name, ErrExists)
		}

		return nil
	}

	return a.DB.WithLock(ctx, policyAssignmentLock, func() error {
		if _, err := a.Policy(ctx, p.Name); err != nil {
			return err
		}

		if err := a.DB.UpsertScanPolicy(
			ctx,
			database.UpsertScanPolicyParams{
				Name: p.Name,
				Kind: string(p.Kind),
				Doc:  doc,
				Now:  now,
			},
		); err != nil {
			return fmt.Errorf("save policy: %w", err)
		}

		return nil
	})
}

const policyAssignmentLock = "scan-policy-assignment"

// DeletePolicy deletes a policy no watch uses.
func (a *Admin) DeletePolicy(ctx context.Context, name string) error {
	return a.DB.WithLock(ctx, policyAssignmentLock, func() error {
		watches, err := a.Watches(ctx)
		if err != nil {
			return err
		}

		for _, w := range watches {
			if slices.Contains(w.Policies, name) {
				return fmt.Errorf("policy %s is assigned to watch %s: %w", name, w.Name, ErrInUse)
			}
		}

		defer a.changed()

		n, err := a.DB.DeleteScanPolicy(ctx, name)
		if err != nil {
			return fmt.Errorf("delete policy: %w", err)
		}

		if n == 0 {
			return fmt.Errorf("policy %s: %w", name, ErrNotFound)
		}

		return nil
	})
}

// Watches lists every watch.
func (a *Admin) Watches(ctx context.Context) ([]Watch, error) {
	rows, err := a.DB.ListScanWatches(ctx)
	if err != nil {
		return nil, fmt.Errorf("list watches: %w", err)
	}

	out := make([]Watch, 0, len(rows))
	for _, r := range rows {
		var w Watch
		if json.Unmarshal(r.Doc, &w) == nil {
			out = append(out, w)
		}
	}

	return out, nil
}

// Watch returns one watch.
func (a *Admin) Watch(ctx context.Context, name string) (Watch, error) {
	row, err := a.DB.GetScanWatch(ctx, name)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return Watch{}, fmt.Errorf("watch %s: %w", name, ErrNotFound)
	}

	if err != nil {
		return Watch{}, fmt.Errorf("get watch: %w", err)
	}

	var w Watch
	if err := json.Unmarshal(row.Doc, &w); err != nil {
		return Watch{}, fmt.Errorf("decode watch %s: %w", name, err)
	}

	return w, nil
}

// SaveWatch creates (create set) or replaces an existing watch.
func (a *Admin) SaveWatch(ctx context.Context, w Watch, create bool) error {
	if err := w.Validate(); err != nil {
		return err
	}

	return a.DB.WithLock(ctx, policyAssignmentLock, func() error {
		for _, name := range w.Policies {
			if _, err := a.Policy(ctx, name); err != nil {
				if errors.Is(err, ErrNotFound) {
					return fmt.Errorf("%w: policy %s does not exist", ErrInvalid, name)
				}

				return err
			}
		}

		doc, err := json.Marshal(w)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		name := w.Name

		defer a.changed()

		if create {
			n, err := a.DB.InsertScanWatch(ctx, database.InsertScanWatchParams{
				Name: name,
				Doc:  doc,
				Now:  now,
			})
			if err != nil {
				return fmt.Errorf("create watch: %w", err)
			}

			if n == 0 {
				return fmt.Errorf("watch %s: %w", name, ErrExists)
			}

			return nil
		}

		if _, err := a.Watch(ctx, name); err != nil {
			return err
		}

		if err := a.DB.UpsertScanWatch(ctx, database.UpsertScanWatchParams{
			Name: name,
			Doc:  doc,
			Now:  now,
		}); err != nil {
			return fmt.Errorf("save watch: %w", err)
		}

		return nil
	})
}

// DeleteWatch deletes a watch.
func (a *Admin) DeleteWatch(ctx context.Context, name string) error {
	return a.DB.WithLock(ctx, policyAssignmentLock, func() error {
		defer a.changed()

		n, err := a.DB.DeleteScanWatch(ctx, name)
		if err != nil {
			return fmt.Errorf("delete watch: %w", err)
		}

		if n == 0 {
			return fmt.Errorf("watch %s: %w", name, ErrNotFound)
		}

		return nil
	})
}

// IgnoreRules lists ignore rules; active limits them to unexpired ones.
func (a *Admin) IgnoreRules(ctx context.Context, active bool) ([]IgnoreRule, error) {
	var docs [][]byte

	if active {
		rows, err := a.DB.ListActiveIgnoreRules(ctx, time.Now().UTC())
		if err != nil {
			return nil, fmt.Errorf("list ignore rules: %w", err)
		}

		for _, r := range rows {
			docs = append(docs, r.Doc)
		}
	} else {
		rows, err := a.DB.ListIgnoreRules(ctx)
		if err != nil {
			return nil, fmt.Errorf("list ignore rules: %w", err)
		}

		for _, r := range rows {
			docs = append(docs, r.Doc)
		}
	}

	out := make([]IgnoreRule, 0, len(docs))
	for _, d := range docs {
		var r IgnoreRule
		if json.Unmarshal(d, &r) == nil {
			out = append(out, r)
		}
	}

	return out, nil
}

// IgnoreRule returns one ignore rule.
func (a *Admin) IgnoreRule(ctx context.Context, id uuid.UUID) (IgnoreRule, error) {
	row, err := a.DB.GetIgnoreRule(ctx, id)
	if errors.Is(database.NotFound(err), database.ErrNotFound) {
		return IgnoreRule{}, fmt.Errorf("ignore rule %s: %w", id, ErrNotFound)
	}

	if err != nil {
		return IgnoreRule{}, fmt.Errorf("get ignore rule: %w", err)
	}

	var r IgnoreRule
	if err := json.Unmarshal(row.Doc, &r); err != nil {
		return IgnoreRule{}, err
	}

	return r, nil
}

// CreateIgnoreRule stores a rule and returns it with its ID.
func (a *Admin) CreateIgnoreRule(ctx context.Context, r IgnoreRule, user string) (IgnoreRule, error) {
	defer a.changed()

	return insertIgnoreRule(ctx, a.DB, r, user)
}

func insertIgnoreRule(ctx context.Context, q database.Querier, r IgnoreRule, user string) (IgnoreRule, error) {
	if r.Empty() {
		return IgnoreRule{}, fmt.Errorf("%w: an ignore rule needs at least one filter", ErrInvalid)
	}

	if p, found := firstInvalidPackage(r.Packages); found {
		return IgnoreRule{}, fmt.Errorf("%w: %q is not a package URL", ErrInvalid, p)
	}

	id := uuid.NewV4()
	r.ID = id.String()

	doc, err := json.Marshal(r)
	if err != nil {
		return IgnoreRule{}, err
	}

	err = q.InsertIgnoreRule(
		ctx,
		database.InsertIgnoreRuleParams{
			ID:        id,
			Doc:       doc,
			ExpiresAt: r.Expires,
			Now:       time.Now().UTC(),
			Username:  user,
		},
	)
	if err != nil {
		return IgnoreRule{}, fmt.Errorf("create ignore rule: %w", err)
	}

	return r, nil
}

// CreateIgnoreRules stores rules in one transaction, all or none, and
// returns them with their IDs.
func (a *Admin) CreateIgnoreRules(ctx context.Context, rules []IgnoreRule, user string) ([]IgnoreRule, error) {
	out := make([]IgnoreRule, 0, len(rules))

	defer a.changed()

	err := a.DB.WithTx(ctx, func(q database.Querier) error {
		out = out[:0]

		for _, r := range rules {
			created, err := insertIgnoreRule(ctx, q, r, user)
			if err != nil {
				return err
			}

			out = append(out, created)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// DeleteIgnoreRule deletes a rule.
func (a *Admin) DeleteIgnoreRule(ctx context.Context, id uuid.UUID) error {
	defer a.changed()

	n, err := a.DB.DeleteIgnoreRule(ctx, id)
	if err != nil {
		return fmt.Errorf("delete ignore rule: %w", err)
	}

	if n == 0 {
		return fmt.Errorf("ignore rule %s: %w", id, ErrNotFound)
	}

	return nil
}
