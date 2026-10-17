//go:build integration

package repository

import (
	"errors"
	"strings"
	"testing"
)

func TestHoldsBlockDeletesMovesAndOverwrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		hold   string
		folder string
		free   string
	}{
		{
			name:   "on a folder",
			hold:   "a",
			folder: "a",
			free:   "b/g.txt",
		},
		{
			name:   "on the repository's root",
			hold:   "",
			folder: "a",
		},
		{
			name:   "on a folder whose name is a LIKE pattern",
			hold:   "a_%",
			folder: "a_%",
			free:   "ab%x/g.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			s, _, _ := newTestService(t)
			file := tt.folder + "/f.txt"
			deploy(t, s, file, "held")
			deploy(t, s, "0/early.txt", "free, listed before the held folder")

			if tt.free != "" {
				deploy(t, s, tt.free, "free")
			}

			if err := s.PutHold(ctx, "generic-local", tt.hold, "litigation 42", "admin"); err != nil {
				t.Fatal(err)
			}

			for name, op := range map[string]func() error{
				"delete the file": func() error {
					return s.Delete(ctx, "generic-local", file)
				},
				"delete the folder": func() error {
					return s.Delete(ctx, "generic-local", tt.folder)
				},
				"empty the repository": func() error {
					return s.Delete(ctx, "generic-local", "")
				},
				"trash the file": func() error {
					return s.DeleteToTrash(ctx, "generic-local", file, "alice")
				},
				"move the folder": func() error {
					folder, err := s.Stat(ctx, "generic-local", tt.folder)
					if err != nil {
						return err
					}

					return s.Copy(ctx, CopyInput{
						Source:                folder,
						DestinationRepository: "generic-local",
						DestinationPath:       "moved",
						Move:                  true,
					})
				},
				"trash the repository": func() error {
					return s.DeleteToTrash(ctx, "generic-local", "", "alice")
				},
				"overwrite the file": func() error {
					_, err := s.Deploy(ctx, DeployInput{
						Repository: "generic-local",
						Path:       file,
						Body:       strings.NewReader("replaced"),
					})

					return err
				},
				"delete the repository": func() error {
					return s.DeleteRepository(ctx, "generic-local")
				},
			} {
				if err := op(); !errors.Is(err, ErrHeld) {
					t.Errorf("%s under a hold = %v, want ErrHeld", name, err)
				}
			}

			if _, err := s.Stat(ctx, "generic-local", "0/early.txt"); err != nil {
				t.Errorf("an item outside the hold is gone after the refused operations: %v", err)
			}

			if tt.free != "" {
				if err := s.Delete(ctx, "generic-local", tt.free); err != nil {
					t.Errorf("delete of an item outside the hold: %v", err)
				}
			}

			holds, err := s.Holds(ctx)
			if err != nil || len(holds) != 1 || holds[0].Reason != "litigation 42" {
				t.Fatalf("Holds = %+v, %v", holds, err)
			}

			if err := s.ReleaseHold(ctx, "generic-local", tt.hold); err != nil {
				t.Fatal(err)
			}

			if err := s.Delete(ctx, "generic-local", file); err != nil {
				t.Errorf("delete after the hold was released: %v", err)
			}
		})
	}
}

func TestImmutableRepositoryRefusesChangedContent(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	s, _, _ := newTestService(t)

	r, err := s.GetRepository(ctx, "generic-local")
	if err != nil {
		t.Fatal(err)
	}

	r.Config.Immutable = true
	if err := s.UpdateRepository(ctx, r); err != nil {
		t.Fatal(err)
	}

	deploy(t, s, "v1/app.bin", "release one")
	deploy(t, s, "v1/app.bin", "release one")

	_, err = s.Deploy(ctx, DeployInput{
		Repository: "generic-local",
		Path:       "v1/app.bin",
		Body:       strings.NewReader("something else"),
	})
	if !errors.Is(err, ErrImmutable) {
		t.Errorf("overwrite with other content = %v, want ErrImmutable", err)
	}

	other := deploy(t, s, "v2/app.bin", "release two")
	if err := s.Copy(ctx, CopyInput{
		Source:                other,
		DestinationRepository: "generic-local",
		DestinationPath:       "v1/app.bin",
	}); !errors.Is(err, ErrImmutable) {
		t.Errorf("copy over a file = %v, want ErrImmutable", err)
	}
}
