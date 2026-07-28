package gomailer

import "testing"

func TestSuppressionReasonString(t *testing.T) {
	tests := []struct {
		reason SuppressionReason
		want   string
	}{
		{SuppressionReasonBounce, "bounce"},
		{SuppressionReasonComplaint, "complaint"},
		{SuppressionReasonUnknown, ""},
		{SuppressionReason("custom"), "custom"},
	}

	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			if got := tt.reason.String(); got != tt.want {
				t.Errorf("SuppressionReason.String() = %q, want %q", got, tt.want)
			}
		})
	}
}
