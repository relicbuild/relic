package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
)

func TestTokenRow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 17, 16, 57, 36, 0, time.Local)

	tests := []struct {
		name        string
		token       *relicv1.AccessToken
		wantExpires string
		wantState   string
	}{
		{
			name:        "a token that never expires",
			token:       &relicv1.AccessToken{},
			wantExpires: "never",
			wantState:   "active",
		},
		{
			name: "a token that expires later today",
			token: &relicv1.AccessToken{
				Expires: timestamppb.New(now.Add(time.Hour)),
			},
			wantExpires: "2026-10-17 17:57",
			wantState:   "active",
		},
		{
			name: "a token that expired seconds ago",
			token: &relicv1.AccessToken{
				Expires: timestamppb.New(now.Add(-5 * time.Second)),
			},
			wantExpires: "2026-10-17 16:57",
			wantState:   "expired",
		},
		{
			name: "a revoked token",
			token: &relicv1.AccessToken{
				Expires: timestamppb.New(now.Add(-time.Hour)),
				Revoked: true,
			},
			wantExpires: "2026-10-17 15:57",
			wantState:   "revoked",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			row := tokenRow(tt.token, now)
			if row[2] != tt.wantExpires || row[3] != tt.wantState {
				t.Errorf("row = %q, want expires %q and state %q", row, tt.wantExpires, tt.wantState)
			}
		})
	}
}
