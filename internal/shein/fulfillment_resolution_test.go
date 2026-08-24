package shein

import "testing"

func TestValidFulfillmentResolutionStatus(t *testing.T) {
	for _, status := range []string{"manually_fulfilled", "cancelled", "not_required", "other"} {
		if !validFulfillmentResolutionStatus(status) {
			t.Fatalf("status %q should be accepted", status)
		}
	}
	for _, status := range []string{"", "resolved", "cancel"} {
		if validFulfillmentResolutionStatus(status) {
			t.Fatalf("status %q should be rejected", status)
		}
	}
}
