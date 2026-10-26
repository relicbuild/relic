//go:build integration

package huggingface

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database/databasetest"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestRemoteAnswersAboveTheLimitAreRefused(t *testing.T) {
	t.Parallel()

	const sha = "0123456789abcdef0123456789abcdef01234567"

	oversized := `[{"path":"` + strings.Repeat("a", maxUpstreamAPIBytes) + `"}]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/api/models/org/small/revision/main":
			_, _ = io.WriteString(w, `{"sha":"`+sha+`"}`)
		case "/api/models/org/small/tree/" + sha:
			_, _ = io.WriteString(w, `[{"path":"config.json","xetHash":"x"}]`)
		case "/api/models/org/large/revision/main":
			_, _ = io.WriteString(w, `{"sha":"`+sha+`","siblings":`+oversized+`}`)
		case "/api/models/org/large/tree/" + sha:
			_, _ = io.WriteString(w, oversized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	log := slog.New(slog.DiscardHandler)
	repos := repository.NewService(databasetest.NewDB(t), objectstoretest.NewMemory(), log)
	repos.AllowPrivateUpstreams(true)

	remote := repository.Repository{
		Name:   "hf-remote",
		Class:  repository.ClassRemote,
		Format: repository.FormatHuggingFace,
		Config: repository.Config{
			Remote: &repository.RemoteConfig{
				URL: srv.URL,
			},
		},
	}
	if err := repos.CreateRepository(t.Context(), remote); err != nil {
		t.Fatal(err)
	}

	service := NewService(repos, log)

	if info, err := service.RemoteInfo(t.Context(), remote, Kind("models"), "org/small", "main"); err != nil ||
		info["sha"] != sha {
		t.Errorf("RemoteInfo of a small answer = %v, %v", info, err)
	}

	if tree, err := service.RemoteTree(
		t.Context(),
		remote,
		Kind("models"),
		"org/small",
		sha,
	); err != nil ||
		len(tree) != 1 {
		t.Errorf("RemoteTree of a small answer = %v, %v", tree, err)
	}

	if _, err := service.RemoteInfo(t.Context(), remote, Kind("models"), "org/large", "main"); !errors.Is(
		err,
		repository.ErrUpstream,
	) {
		t.Errorf("RemoteInfo above the limit = %v, want ErrUpstream", err)
	}

	if _, err := service.RemoteTree(t.Context(), remote, Kind("models"), "org/large", sha); !errors.Is(
		err,
		repository.ErrUpstream,
	) {
		t.Errorf("RemoteTree above the limit = %v, want ErrUpstream", err)
	}
}
