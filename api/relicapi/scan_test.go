package relicapi

import (
	"testing"

	"google.golang.org/protobuf/proto"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/scanning/scan/policy"
)

func TestPolicyOfOpensAMissingCVSSBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rule *relicv1.PolicyRule
		want policy.CVSSRange
	}{
		{
			name: "from only",
			rule: &relicv1.PolicyRule{
				Name:     "high",
				CvssFrom: proto.Float64(7),
			},
			want: policy.CVSSRange{
				From: 7,
				To:   10,
			},
		},
		{
			name: "to only",
			rule: &relicv1.PolicyRule{
				Name:   "low",
				CvssTo: proto.Float64(4),
			},
			want: policy.CVSSRange{
				From: 0,
				To:   4,
			},
		},
		{
			name: "both",
			rule: &relicv1.PolicyRule{
				Name:     "medium",
				CvssFrom: proto.Float64(4),
				CvssTo:   proto.Float64(6.9),
			},
			want: policy.CVSSRange{
				From: 4,
				To:   6.9,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := policyOf(&relicv1.Policy{
				Name: "cvss",
				Kind: relicv1.PolicyKind_POLICY_KIND_SECURITY,
				Rules: []*relicv1.PolicyRule{
					tt.rule,
				},
			})

			if got := p.Rules[0].CVSS; got == nil || *got != tt.want {
				t.Errorf("CVSS = %v, want %v", got, tt.want)
			}
		})
	}
}
