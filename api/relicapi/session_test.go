package relicapi

import (
	"testing"

	relicv1 "relic/api/gen/relic/v1"
)

func TestGetSignInNote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		note string
	}{
		{
			name: "configured note",
			note: "Sign in as demo / demo. This demo is read-only.",
		},
		{
			name: "no note",
			note: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &sessionServer{
				d: Dependencies{
					SignInNote: tt.note,
				},
			}

			resp, err := s.GetSignInNote(t.Context(), &relicv1.GetSignInNoteRequest{})
			if err != nil {
				t.Fatal(err)
			}

			if got := resp.GetNote(); got != tt.note {
				t.Errorf("note = %q, want %q", got, tt.note)
			}
		})
	}
}
