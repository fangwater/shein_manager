package shein

import "testing"

func TestIsAllowedShippingWarehouseAcceptsOperatedWarehouses(t *testing.T) {
	cases := [][2]string{
		{"DPSNY002", "DPS达派思-纽约"},
		{"HYTX30", "ARP1号仓-美东PA"},
		{"DPSCA004", "DPS达派思-加州"},
		{"ARPCA01", "ARP8号仓-美西LA"},
		{"WH2604283535967233", ""},
		{"WH2603303477748739", ""},
		{"WH2607084039788546", ""},
		{"WH2608123417047040", ""},
		{"WH2604283535967233", "DPSNY002（美东）"},
		{"WH2607084039788546", "ARP仓-美东"},
	}
	for _, item := range cases {
		if !IsAllowedShippingWarehouse(item[0], item[1]) {
			t.Fatalf("operated warehouse must stay selectable: %#v", item)
		}
	}
}

func TestIsAllowedShippingWarehouseRejectsPGAndUnknownWarehouses(t *testing.T) {
	cases := [][2]string{
		{"PG1955", "PG仓"},
		{"USPG001", "SHEIN PG仓"},
		{"", "PG1955 美西"},
		{"WH2602103441974274", ""},
		{"WH2602103441974274", "PG仓"},
		{"WH2602103360052227", "美东"},
		{"WH-1", "未知仓"},
		{"", ""},
	}
	for _, item := range cases {
		if IsAllowedShippingWarehouse(item[0], item[1]) {
			t.Fatalf("non-operated warehouse must not be selectable: %#v", item)
		}
	}
	if !IsPGWarehouse("PG1955", "平台仓") {
		t.Fatal("PG1955 must be recognized as a PG warehouse")
	}
	if !IsPGWarehouse("WH2602103441974274", "") {
		t.Fatal("live PG warehouse address code must be recognized without a name")
	}
}

func TestOMSAccountForWarehouseUsesOperatedOwnership(t *testing.T) {
	if got := OMSAccountForWarehouse("WH2604283535967233", "DPSNY002（美东）"); got != "dps" {
		t.Fatalf("DPS account = %q", got)
	}
	if got := ResolvedOMSWarehouseCode("WH2607084039788546", ""); got != "HYTX30" {
		t.Fatalf("ARP east OMS warehouse = %q", got)
	}
	if got := ResolvedOMSWarehouseCode("WH2608123417047040", "ARP-美西仓"); got != "ARPCA01" {
		t.Fatalf("ARP west OMS warehouse = %q", got)
	}
	if got := OMSAccountForWarehouse("WH2607084039788546", "ARP仓-美东"); got != "arp" {
		t.Fatalf("ARP account = %q", got)
	}
	if got := OMSAccountForWarehouse("PG1955", "PG仓"); got != "" {
		t.Fatalf("PG account = %q", got)
	}
	if OppositeOMSAccount("dps") != "arp" || OppositeOMSAccount("ARP") != "dps" {
		t.Fatal("opposite OMS account mapping is wrong")
	}
}

func TestResolvePurchasedWarehouseFollowsBoughtLabelOnly(t *testing.T) {
	west := ResolvePurchasedWarehouse("WH2603303477748739")
	if !west.OK() || west.Account != "dps" || west.OMSCode != "DPSCA004" {
		t.Fatalf("DPS west label must stay DPSCA004: %#v", west)
	}
	east := ResolvePurchasedWarehouse("WH2604283535967233")
	if !east.OK() || east.Account != "dps" || east.OMSCode != "DPSNY002" {
		t.Fatalf("DPS east label must stay DPSNY002: %#v", east)
	}
	directARP := ResolvePurchasedWarehouse("WH2607084039788546")
	if !directARP.OK() || directARP.Account != "arp" || directARP.OMSCode != "HYTX30" {
		t.Fatalf("ARP label must map to HYTX30: %#v", directARP)
	}
	if ResolvePurchasedWarehouse("WH-UNKNOWN").OK() {
		t.Fatal("unknown label warehouse must not resolve")
	}
}

func TestRequiresManualParcelCreateIsShopScopedToDPS(t *testing.T) {
	if !RequiresManualParcelCreate("beauty-hangers-home", "WH2604283535967233", "DPSNY002（美东）") {
		t.Fatal("Beauty Hangers DPS warehouse must require a manual XLWMS parcel")
	}
	if !RequiresManualParcelCreate("beauty-hangers-home", "DPSCA004", "DPS达派思-加州") {
		t.Fatal("Beauty Hangers west DPS warehouse must require a manual XLWMS parcel")
	}
	if RequiresManualParcelCreate("beauty-hangers-home", "WH2607084039788546", "ARP仓-美东") {
		t.Fatal("Beauty Hangers ARP warehouse must not require a manual XLWMS parcel")
	}
	if RequiresManualParcelCreate("other-shop", "WH2604283535967233", "DPSNY002（美东）") {
		t.Fatal("unconfigured shops must not inherit the Beauty Hangers DPS rule")
	}
}

func TestRestrictShippingWarehouseAvailabilityDisablesPGWarehouses(t *testing.T) {
	result := map[string]any{"info": map[string]any{"availableWarehouses": []any{
		map[string]any{"warehouseAddressCode": "WH2604283535967233", "warehouseName": "DPSNY002（美东）", "availableStatus": "1"},
		map[string]any{"warehouseAddressCode": "WH2602103441974274", "warehouseName": "PG仓", "availableStatus": "1"},
		map[string]any{"warehouseAddressCode": "WH2602103360052227", "warehouseName": "美东", "availableStatus": "1"},
	}}}
	RestrictShippingWarehouseAvailability(result)
	warehouses := warehouseObjects(result["info"])
	if len(warehouses) != 3 {
		t.Fatalf("warehouse count = %d, want 3", len(warehouses))
	}
	if warehouses[0]["availableStatus"] != "1" {
		t.Fatalf("DPS warehouse was disabled: %#v", warehouses[0])
	}
	if warehouses[1]["availableStatus"] != "0" || warehouses[1]["unavailableReason"] != pgWarehouseUnavailableReason {
		t.Fatalf("PG warehouse was left selectable: %#v", warehouses[1])
	}
	if warehouses[2]["availableStatus"] != "0" || warehouses[2]["unavailableReason"] != unoperatedWarehouseUnavailableReason {
		t.Fatalf("unknown warehouse was left selectable: %#v", warehouses[2])
	}
}
