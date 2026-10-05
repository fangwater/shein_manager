package shein

import "testing"

func TestHoustonCarrierCapabilityOverridesExpandedPlatformPolicy(t *testing.T) {
	group := WarehouseCarrierPolicies{BaseRules: WarehouseCarrierRules{WarehouseKey: "ARP_HOUSTON", AllowedCarrierCodes: []string{"USPS", "GOFO", "UPS", "FEDEX", "SPEEDX", "YANWEN", "CBS"}}}
	for _, carrier := range []string{"USPS", "GOFO", "UPS", "FEDEX", "SPEEDX", "YANWEN", "CBS", "unknown"} {
		reason := ChannelUnavailableReason(carrier, "", "", "USD", "ARP06A", "", false, group)
		want := carrier == "USPS" || carrier == "GOFO" || carrier == "UPS" || carrier == "FEDEX"
		if (reason == "") != want {
			t.Fatalf("carrier %s: %s", carrier, reason)
		}
	}
	if PolicyWarehouseKey("ARP06A", "") != "ARP_HOUSTON" || PolicyWarehouseKey("ARPGA", "") != "ARP_ATLANTA" {
		t.Fatal("new warehouse identity was lost")
	}
	disabled := false
	group.WarehouseEnabled = &disabled
	if ChannelUnavailableReason("USPS", "", "", "USD", "ARPGA", "", false, group) == "" {
		t.Fatal("disabled warehouse accepted manual carrier")
	}
}
