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

func TestChannelUnavailableReasonUsesWarehousePolicy(t *testing.T) {
	policies := testCarrierPolicies("DPS002")
	policies[0].Enabled = false
	group := testWarehouseCarrierPolicies("DPS002", policies)
	reason := ChannelUnavailableReason("GOFO-D2D250718-Na", "GOFO", "", "USD", "WH2604283535967233", "DPSNY002（美东）", false, group)
	if reason == "" || reason != "SHEIN 发货策略已在 DPS002 仓库禁用 GOFO" {
		t.Fatalf("unexpected disable reason: %q", reason)
	}
	if got := ChannelUnavailableReason("UNIUNI-1", "", "", "USD", "WH2604283535967233", "", false, group); got != "XLWMS DPS002 基础规则未允许承运商 UNIUNI" {
		t.Fatalf("UNIUNI reason = %q", got)
	}
	if got := ChannelUnavailableReason("OTHER-1", "Other", "", "USD", "WH2604283535967233", "", false, group); got != "XLWMS DPS002 基础规则未允许承运商 OTHER-1" {
		t.Fatalf("unknown carrier reason = %q", got)
	}
	if got := ChannelUnavailableReason("UPS-GROUND", "UPS", "", "USD", "WH2604283535967233", "", false, group); got != "" {
		t.Fatalf("enabled UPS was blocked: %q", got)
	}
}

func TestApplyCarrierPoliciesToChannelsMarksDisabled(t *testing.T) {
	policies := testCarrierPolicies("ARP_EAST")
	for index := range policies {
		if policies[index].CarrierCode == "SPEEDX" {
			policies[index].Enabled = false
		}
	}
	result := map[string]any{"info": map[string]any{"channelInfoList": []any{
		map[string]any{"expressChannelCode": "SPEEDX-US", "expressShortName": "SpeedX"},
		map[string]any{"expressChannelCode": "GOFO-D2D250718-Na", "expressShortName": "GOFO"},
	}}}
	ApplyCarrierPoliciesToChannels(result, "WH2607084039788546", "ARP仓-美东", testWarehouseCarrierPolicies("ARP_EAST", policies))
	channels := channelObjects(result["info"])
	if channels[0]["availableStatus"] != "0" || channels[0]["unavailableReason"] != "SHEIN 发货策略已在 ARP_EAST 仓库禁用 SPEEDX" {
		t.Fatalf("SPEEDX was left selectable: %#v", channels[0])
	}
	if channels[1]["availableStatus"] == "0" {
		t.Fatalf("GOFO was disabled: %#v", channels[1])
	}
}

func testCarrierPolicies(warehouseKey string) []CarrierPolicy {
	codes := []string{"GOFO", "SWIFTX", "SPEEDX", "YANWEN", "UPS", "USPS", "FEDEX"}
	policies := make([]CarrierPolicy, 0, len(codes))
	for index, code := range codes {
		policies = append(policies, CarrierPolicy{WarehouseKey: warehouseKey, CarrierCode: code, Priority: index + 1, Enabled: true})
	}
	return policies
}

func testWarehouseCarrierPolicies(warehouseKey string, policies []CarrierPolicy) WarehouseCarrierPolicies {
	return WarehouseCarrierPolicies{
		WarehouseKey: warehouseKey,
		BaseRules: WarehouseCarrierRules{
			WarehouseKey: warehouseKey, AllowedCarrierCodes: []string{"GOFO", "SWIFTX", "SPEEDX", "YANWEN", "UPS", "USPS", "FEDEX"},
			AllowSignature: true, SelectionMode: "lowest_price", WarehouseTiePriority: 1,
		},
		Carriers: policies,
	}
}
