package shein

import "testing"

func TestHoustonCarrierCapabilityOverridesExpandedPlatformPolicy(t *testing.T) {
	group := WarehouseCarrierPolicies{BaseRules: WarehouseCarrierRules{WarehouseKey: "ARP_HOUSTON", AllowedCarrierCodes: []string{"USPS", "GOFO", "UPS", "FEDEX", "SPEEDX", "YANWEN", "CBS"}}}
	for _, carrier := range []string{"USPS", "GOFO", "UPS", "FEDEX", "SPEEDX", "YANWEN", "CBS", "unknown"} {
		reason := ChannelUnavailableReason(carrier, "", "", "USD", "ARP06A", "", false, group)
		want := carrier == "USPS" || carrier == "GOFO" || carrier == "UPS" || carrier == "FEDEX" || carrier == "SPEEDX" || carrier == "CBS"
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

func TestCollectionWarehouseNewChannelsRespectCarrierPolicy(t *testing.T) {
	for _, key := range []string{"ARP_HOUSTON", "ARP_ATLANTA"} {
		for _, carrier := range []string{"SPEEDX", "CBS"} {
			group := WarehouseCarrierPolicies{WarehouseKey: key, BaseRules: WarehouseCarrierRules{WarehouseKey: key, AllowedCarrierCodes: []string{carrier}}, Carriers: []CarrierPolicy{{WarehouseKey: key, CarrierCode: carrier, Enabled: true}}}
			channel := carrier + "-US-GROUND"
			if reason := ChannelUnavailableReason(channel, "", "", "USD", "WH-DYNAMIC", "", false, group); reason != "" {
				t.Fatalf("%s rejected %s: %s", key, carrier, reason)
			}
			group.Carriers[0].Enabled = false
			if ChannelUnavailableReason(channel, "", "", "USD", "WH-DYNAMIC", "", false, group) == "" {
				t.Fatalf("%s bypassed disabled %s policy", key, carrier)
			}
		}
	}
}
