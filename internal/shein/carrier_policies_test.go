package shein

import "testing"

func TestPolicyWarehouseKeyMapsOperatedWarehouses(t *testing.T) {
	cases := []struct {
		code, name, want string
	}{
		{"WH2604283535967233", "DPSNY002（美东）", "DPS002"},
		{"WH2607084039788546", "ARP仓-美东", "ARP_EAST"},
		{"WH2603303477748739", "", "DPS004"},
		{"WH2608123417047040", "ARP-美西仓", "ARP_WEST"},
		{"PG1955", "PG仓", ""},
	}
	for _, item := range cases {
		if got := PolicyWarehouseKey(item.code, item.name); got != item.want {
			t.Fatalf("PolicyWarehouseKey(%q, %q) = %q, want %q", item.code, item.name, got, item.want)
		}
	}
}

func TestCarrierCodeRecognizesSHEINChannelNames(t *testing.T) {
	if got := CarrierCode("GOFO-D2D250718-Na", "GOFO"); got != "GOFO" {
		t.Fatalf("GOFO channel = %q", got)
	}
	if got := CarrierCodeFromChannel(map[string]any{
		"expressChannelCode": "YANWEN-US-STD", "expressShortName": "Yanwen",
	}); got != "YANWEN" {
		t.Fatalf("Yanwen channel = %q", got)
	}
	if got := CarrierCode("UNIUNI-US"); got != "UNIUNI" {
		t.Fatalf("UNIUNI channel = %q", got)
	}
}

func TestValidateCarrierPoliciesRequiresCompleteUniqueOrder(t *testing.T) {
	policies := DefaultCarrierPolicies("DPS002")
	policies[1].Priority = policies[0].Priority
	if _, err := ValidateCarrierPolicies("DPS002", policies); err == nil {
		t.Fatal("duplicate priorities must fail")
	}
	if _, err := ValidateCarrierPolicies("UNKNOWN", DefaultCarrierPolicies("DPS002")); err == nil {
		t.Fatal("unknown warehouse must fail")
	}
}

func TestMergeCarrierPoliciesFillsMissingWarehouses(t *testing.T) {
	groups := MergeCarrierPolicies(nil)
	if len(groups) != 4 || groups[0].WarehouseKey != "DPS002" || len(groups[0].Carriers) != 7 {
		t.Fatalf("unexpected default groups: %#v", groups)
	}
	if groups[0].Carriers[0].CarrierCode != "GOFO" || !groups[0].Carriers[0].Enabled {
		t.Fatalf("default GOFO policy = %#v", groups[0].Carriers[0])
	}
}

func TestDefaultCarrierPoliciesDisableARPEastSwiftX(t *testing.T) {
	east := DefaultCarrierPolicies("ARP_EAST")
	found := false
	for _, policy := range east {
		if policy.CarrierCode == "SWIFTX" {
			found = true
			if policy.Enabled {
				t.Fatal("ARP east SwiftX must start disabled")
			}
		}
	}
	if !found {
		t.Fatal("ARP east is missing SwiftX")
	}
	if DefaultCarrierEnabled("ARP_EAST", "UNIUNI") {
		t.Fatal("ARP east UNIUNI must stay disabled")
	}
	for _, warehouse := range []string{"DPS002", "DPS004", "ARP_WEST"} {
		for _, policy := range DefaultCarrierPolicies(warehouse) {
			if policy.CarrierCode == "SWIFTX" && !policy.Enabled {
				t.Fatalf("%s SwiftX should stay enabled by default: %#v", warehouse, policy)
			}
		}
	}
}

func TestChannelUnavailableReasonUsesWarehousePolicy(t *testing.T) {
	policies := DefaultCarrierPolicies("DPS002")
	policies[0].Enabled = false
	reason := ChannelUnavailableReason("GOFO-D2D250718-Na", "GOFO", "", "WH2604283535967233", "DPSNY002（美东）", policies)
	if reason == "" || reason != "GOFO 店铺策略已在 DPS002 仓库禁用" {
		t.Fatalf("unexpected disable reason: %q", reason)
	}
	if got := ChannelUnavailableReason("UNIUNI-1", "", "", "WH2604283535967233", "", policies); got != "UNIUNI 已禁用" {
		t.Fatalf("UNIUNI reason = %q", got)
	}
	if got := ChannelUnavailableReason("OTHER-1", "Other", "", "WH2604283535967233", "", policies); got != "不在自动发货物流白名单" {
		t.Fatalf("unknown carrier reason = %q", got)
	}
	if got := ChannelUnavailableReason("UPS-GROUND", "UPS", "", "WH2604283535967233", "", policies); got != "" {
		t.Fatalf("enabled UPS was blocked: %q", got)
	}
}

func TestApplyCarrierPoliciesToChannelsMarksDisabled(t *testing.T) {
	policies := DefaultCarrierPolicies("ARP_EAST")
	for index := range policies {
		if policies[index].CarrierCode == "SPEEDX" {
			policies[index].Enabled = false
		}
	}
	result := map[string]any{"info": map[string]any{"channelInfoList": []any{
		map[string]any{"expressChannelCode": "SPEEDX-US", "expressShortName": "SpeedX"},
		map[string]any{"expressChannelCode": "GOFO-D2D250718-Na", "expressShortName": "GOFO"},
	}}}
	ApplyCarrierPoliciesToChannels(result, "WH2607084039788546", "ARP仓-美东", policies)
	channels := channelObjects(result["info"])
	if channels[0]["availableStatus"] != "0" || channels[0]["unavailableReason"] != "SPEEDX 店铺策略已在 ARP_EAST 仓库禁用" {
		t.Fatalf("SPEEDX was left selectable: %#v", channels[0])
	}
	if channels[1]["availableStatus"] == "0" {
		t.Fatalf("GOFO was disabled: %#v", channels[1])
	}
}
