package shein

import "testing"

func TestOrderHistoryPredicateSeparatesOpenAndHistoricalStatuses(t *testing.T) {
	open := orderHistoryPredicate(false)
	history := orderHistoryPredicate(true)
	if open == history {
		t.Fatal("open and historical predicates must differ")
	}
	if history != "NOT "+open {
		t.Fatalf("history predicate = %q, want NOT of %q", history, open)
	}
	for _, fragment := range []string{"'1', '2'", "pending_processing", "pending_shipping"} {
		if !containsSQLFragment(open, fragment) {
			t.Fatalf("open predicate %q does not contain %q", open, fragment)
		}
	}
}

func containsSQLFragment(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
