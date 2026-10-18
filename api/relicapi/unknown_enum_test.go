package relicapi

import (
	"testing"

	"connectrpc.com/connect/v2"

	relicv1 "relic/api/gen/relic/v1"
)

func TestUnknownEnumValuesAreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "policy decision kind",
			call: func() error {
				_, err := (&policyDecisionServer{}).ListPolicyDecisions(
					t.Context(),
					&relicv1.ListPolicyDecisionsRequest{
						Kind: relicv1.PolicyDecisionKind(99),
					},
				)

				return err
			},
		},
		{
			name: "repository class",
			call: func() error {
				_, err := (&repositoryServer{}).ListRepositories(
					t.Context(),
					&relicv1.ListRepositoriesRequest{
						Class: relicv1.RepositoryClass(99),
					},
				)

				return err
			},
		},
		{
			name: "webhook format",
			call: func() error {
				_, err := domainWebhook(&relicv1.Webhook{
					Name:   "hook",
					Format: relicv1.WebhookFormat(99),
				})

				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := tt.call(); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("err = %v, want InvalidArgument", err)
			}
		})
	}
}
