//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestDeployEnforcesTheUploadLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		global     int64
		repository int64
		content    string
		size       int64
		wantErr    bool
	}{
		{
			name:    "no limit",
			content: "0123456789abcdef",
		},
		{
			name:    "within the server's limit",
			global:  16,
			content: "0123456789abcdef",
		},
		{
			name:    "over the server's limit, length unknown",
			global:  10,
			content: "0123456789abcdef",
			wantErr: true,
		},
		{
			name:    "over the server's limit, length declared",
			global:  10,
			content: "0123456789abcdef",
			size:    16,
			wantErr: true,
		},
		{
			name:       "the repository's lower limit applies",
			global:     100,
			repository: 10,
			content:    "0123456789abcdef",
			wantErr:    true,
		},
		{
			name:       "the server's lower limit applies",
			global:     10,
			repository: 100,
			content:    "0123456789abcdef",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, _, _ := newTestService(t)
			s.SetMaxUploadSize(tt.global)

			if tt.repository > 0 {
				r, err := s.GetRepository(t.Context(), "generic-local")
				if err != nil {
					t.Fatal(err)
				}

				r.Config.MaxUploadSize = tt.repository
				if err := s.UpdateRepository(t.Context(), r); err != nil {
					t.Fatal(err)
				}
			}

			_, err := s.Deploy(
				t.Context(),
				DeployInput{
					Repository: "generic-local",
					Path:       "big.bin",
					Body:       strings.NewReader(tt.content),
					Size:       tt.size,
				},
			)

			if got := errors.Is(err, ErrTooLarge); got != tt.wantErr {
				t.Fatalf("Deploy = %v, want ErrTooLarge %v", err, tt.wantErr)
			}

			if !tt.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func setQuota(t *testing.T, s *Service, quota int64) {
	t.Helper()

	r, err := s.GetRepository(t.Context(), "generic-local")
	if err != nil {
		t.Fatal(err)
	}

	r.Config.MaxStorage = quota
	if err := s.UpdateRepository(t.Context(), r); err != nil {
		t.Fatal(err)
	}
}

func TestStorageQuota(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)
	setQuota(t, s, 30)

	for i := range 3 {
		deploy(t, s, fmt.Sprintf("f%d.bin", i), fmt.Sprintf("ten bytes%d", i))
	}

	put := func(path, content string) error {
		_, err := s.Deploy(ctx, DeployInput{
			Repository: "generic-local",
			Path:       path,
			Body:       strings.NewReader(content),
		})

		return err
	}

	if err := put("over.bin", "x"); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("deploy past the quota = %v, want ErrQuotaExceeded", err)
	}

	if _, err := s.DeployByChecksum(ctx, DeployInput{
		Repository: "generic-local",
		Path:       "same.bin",
		Expected: Checksums{
			SHA256: sha256Hex("ten bytes0"),
		},
	}); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("checksum deploy past the quota = %v, want ErrQuotaExceeded", err)
	}

	f0, err := s.Stat(ctx, "generic-local", "f0.bin")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Copy(ctx, CopyInput{
		Source:                f0,
		DestinationRepository: "generic-local",
		DestinationPath:       "copy.bin",
	}); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("copy past the quota = %v, want ErrQuotaExceeded", err)
	}

	if err := s.Delete(ctx, "generic-local", "f0.bin"); err != nil {
		t.Fatal(err)
	}

	if err := put("again.bin", "ten bytes!"); err != nil {
		t.Errorf("deploy after freeing room = %v", err)
	}
}

func TestConcurrentDeploysCannotOvershootTheQuota(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	setQuota(t, s, 50)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ok      int
		refused int
	)

	for i := range 10 {
		wg.Go(func() {
			_, err := s.Deploy(context.Background(), DeployInput{
				Repository: "generic-local",
				Path:       fmt.Sprintf("c%d.bin", i),
				Body:       strings.NewReader(fmt.Sprintf("ten bytes%d", i)),
			})

			mu.Lock()
			defer mu.Unlock()

			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrQuotaExceeded):
				refused++
			default:
				t.Errorf("deploy %d: %v", i, err)
			}
		})
	}

	wg.Wait()

	if ok != 5 || refused != 5 {
		t.Errorf("%d deploys succeeded and %d were refused, want 5 and 5", ok, refused)
	}
}
