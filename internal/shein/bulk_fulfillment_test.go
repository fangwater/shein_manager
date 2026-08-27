package shein

import "testing"

func TestBulkFulfillmentBatchStatusContinuesPastFailedOrder(t *testing.T) {
	if got := bulkFulfillmentBatchStatus(1, 3); got != "running" {
		t.Fatalf("batch status = %q, want running", got)
	}
	if got := bulkFulfillmentBatchStatus(1, 0); got != "completed_with_errors" {
		t.Fatalf("batch status = %q, want completed_with_errors", got)
	}
	if got := bulkFulfillmentBatchStatus(0, 0); got != "completed" {
		t.Fatalf("batch status = %q, want completed", got)
	}
}
