package buildinfo

import (
	"context"
	"fmt"
	"time"
)

// Link is a build run that produced or used some content.
type Link struct {
	Project string
	Name    string
	Number  string
	Started time.Time
	Module  string
	Scopes  string // how a dependency was used; empty for artifacts
}

// Producing lists the build runs that published content with sha1.
func (s *Service) Producing(ctx context.Context, sha1 string) ([]Link, error) {
	rows, err := s.db.BuildsProducing(ctx, sha1)
	if err != nil {
		return nil, fmt.Errorf("builds producing %s: %w", sha1, err)
	}

	out := make([]Link, 0, len(rows))
	for _, b := range rows {
		out = append(
			out,
			Link{
				Project: b.Project,
				Name:    b.Name,
				Number:  b.Number,
				Started: b.Started,
				Module:  b.ModuleID,
			},
		)
	}

	return out, nil
}

// Using lists the build runs that depended on content with sha1.
func (s *Service) Using(ctx context.Context, sha1 string) ([]Link, error) {
	rows, err := s.db.BuildsUsing(ctx, sha1)
	if err != nil {
		return nil, fmt.Errorf("builds using %s: %w", sha1, err)
	}

	out := make([]Link, 0, len(rows))
	for _, b := range rows {
		out = append(
			out,
			Link{
				Project: b.Project,
				Name:    b.Name,
				Number:  b.Number,
				Started: b.Started,
				Module:  b.ModuleID,
				Scopes:  b.Scopes,
			},
		)
	}

	return out, nil
}
