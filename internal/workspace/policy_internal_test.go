package workspace

import (
	"strings"
	"testing"
)

func TestAttemptBoundaryPolicyValidateStrengthens(t *testing.T) {
	t.Parallel()

	policy := func(escalation AttemptEscalationPolicy) AttemptBoundaryPolicy {
		return AttemptBoundaryPolicy{escalation: escalation}
	}
	profileBoundary := func(escalation AttemptEscalationPolicy) ProfileBoundaryPolicy {
		return ProfileBoundaryPolicy{escalation: escalation}
	}
	tests := []struct {
		name    string
		base    ProfileBoundaryPolicy
		policy  AttemptBoundaryPolicy
		wantErr string
	}{
		{
			name:    "escalation forbidden to allowed is rejected",
			base:    profileBoundary(AttemptEscalationForbidden),
			policy:  policy(AttemptEscalationAllowed),
			wantErr: "weakens escalation",
		},
		{
			name:   "escalation allowed to forbidden is accepted",
			base:   profileBoundary(AttemptEscalationAllowed),
			policy: policy(AttemptEscalationForbidden),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.policy.validateStrengthens(test.base, "merge unit boundary")
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateStrengthens: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateStrengthens error = %v", err)
			}
		})
	}
}
