package sheinconsole

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"shein-api-manager/internal/shein"
)

func TestCollectOrderObjectsFindsNestedListsAndDetails(t *testing.T) {
	value := map[string]any{"data": map[string]any{"records": []any{
		map[string]any{"orderNo": "ORDER-1", "orderStatus": float64(1)},
	}}}
	if orders := collectOrderObjects(value, false); len(orders) != 1 || orderNumberFromMap(orders[0]) != "ORDER-1" {
		t.Fatalf("nested order list was not found: %#v", orders)
	}
	if details := collectOrderObjects(value, true); len(details) != 0 {
		t.Fatalf("list row was mistaken for detail: %#v", details)
	}
}

func TestChannelRequestUsesWarehouseSpecAndConvertsWeight(t *testing.T) {
	request, err := channelRequest("ORDER-1", "WH2604283535967233", "DPSNY002（美东）", shein.PackageSpec{
		LengthCM: "20", WidthCM: "15", HeightCM: "5", WeightKG: "0.3",
	}, []shein.QueueGoods{{GoodsID: "GOODS-1"}}, false)
	if err != nil {
		t.Fatalf("channelRequest returned error: %v", err)
	}
	weight := request["packageWeightInfo"].(map[string]any)
	if weight["packageWeight"] != "300" {
		t.Fatalf("package weight = %v, want 300g", weight["packageWeight"])
	}
	if _, ok := request["prePackageInfo"]; ok {
		t.Fatalf("non-COD channel request must omit goods details: %#v", request)
	}
	if request["warehouseAddressCode"] != "WH2604283535967233" || request["warehouseName"] != "DPSNY002（美东）" {
		t.Fatalf("channel request warehouse identity = %#v", request)
	}
	codRequest, err := channelRequest("ORDER-1", "WH2604283535967233", "DPSNY002（美东）", shein.PackageSpec{
		LengthCM: "20", WidthCM: "15", HeightCM: "5", WeightKG: "0.3",
	}, []shein.QueueGoods{{GoodsID: "GOODS-1"}}, true)
	if err != nil {
		t.Fatalf("COD channelRequest returned error: %v", err)
	}
	prePackage := codRequest["prePackageInfo"].(map[string]any)
	if len(prePackage["goodsIds"].([]any)) != 1 {
		t.Fatalf("COD goods IDs missing from channel request: %#v", codRequest)
	}
}

func TestLowestQuotedChannelComparesWarehouses(t *testing.T) {
	quotes := []quotedChannel{
		{
			Quote: shein.ShippingQuote{WarehouseAddressCode: "WH-B"},
			Candidate: shein.ShippingQuoteCandidate{
				ExpressChannelCode: "CHANNEL-B", PerformanceCost: "12.50", CurrencyCode: "USD",
			},
		},
		{
			Quote: shein.ShippingQuote{WarehouseAddressCode: "WH-A"},
			Candidate: shein.ShippingQuoteCandidate{
				ExpressChannelCode: "CHANNEL-A", PerformanceCost: "9.80", CurrencyCode: "USD",
			},
		},
	}
	selected, err := lowestQuotedChannel(quotes)
	if err != nil {
		t.Fatalf("lowestQuotedChannel returned error: %v", err)
	}
	if selected.Quote.WarehouseAddressCode != "WH-A" || selected.Candidate.ExpressChannelCode != "CHANNEL-A" {
		t.Fatalf("wrong automatic quote selected: %#v", selected)
	}
}

func TestPlatformLabelPurchaseSkipsAddressTransition(t *testing.T) {
	pendingIntegrated := map[string]any{
		"optionalLogisticsList": []any{float64(1)},
		"performanceType":       float64(2),
		"orderStatus":           float64(1),
	}
	if !shein.CanPurchasePlatformLabel(pendingIntegrated) {
		t.Fatal("pending integrated order should purchase a platform label")
	}
	if shein.RequiresAddressTransition(pendingIntegrated, "1") {
		t.Fatal("automatic fulfillment must not export-address a platform-logistics order")
	}
}

func TestAvailableWarehousesKeepsOnlyOperatedWarehouses(t *testing.T) {
	warehouses := availableWarehouses(map[string]any{"info": map[string]any{"availableWarehouses": []any{
		map[string]any{"warehouseAddressCode": "WH2602103441974274", "warehouseName": "PG仓", "availableStatus": "1"},
		map[string]any{"warehouseAddressCode": "WH2604283535967233", "warehouseName": "DPSNY002（美东）", "availableStatus": "1"},
		map[string]any{"warehouseAddressCode": "WH2602103360052227", "warehouseName": "美东", "availableStatus": "1"},
		map[string]any{"warehouseAddressCode": "WH2608123417047040", "warehouseName": "ARP-美西仓", "availableStatus": "0"},
	}}})
	if len(warehouses) != 1 || scalarString(warehouses[0], "warehouseAddressCode") != "WH2604283535967233" {
		t.Fatalf("automatic quoting must ignore PG and unavailable warehouses: %#v", warehouses)
	}
}

func TestAutomaticInventoryWarehouseKeysExcludesZeroStockARPEast(t *testing.T) {
	decision := json.RawMessage(`{
		"complete":true,
		"package_resolution":{"complete":true},
		"records":[{"sku":"WH-SKU","requires_manual":false,"regions":[
			{"warehouses":[
				{"warehouse_key":"DPS002","active":true,"query_status":"succeeded","available_amount":8,"selectable":true},
				{"warehouse_key":"ARP_EAST","active":true,"query_status":"succeeded","available_amount":0,"selectable":false}
			]},
			{"warehouses":[
				{"warehouse_key":"DPS004","active":true,"query_status":"succeeded","available_amount":3,"selectable":true},
				{"warehouse_key":"ARP_WEST","active":true,"query_status":"succeeded","available_amount":0,"selectable":false}
			]}
		]}]
	}`)
	eligible, err := automaticInventoryWarehouseKeys(decision, map[string]int{"WH-SKU": 1})
	if err != nil {
		t.Fatalf("automaticInventoryWarehouseKeys returned error: %v", err)
	}
	if !eligible["DPS002"] || !eligible["DPS004"] || eligible["ARP_EAST"] || eligible["ARP_WEST"] {
		t.Fatalf("unexpected eligible warehouses: %#v", eligible)
	}

	warehouses := []map[string]any{
		{"warehouseAddressCode": "WH2604283535967233", "warehouseName": "DPSNY002（美东）"},
		{"warehouseAddressCode": "WH2607084039788546", "warehouseName": "ARP仓-美东"},
	}
	filtered := warehousesWithInventory(warehouses, eligible)
	if len(filtered) != 1 || scalarString(filtered[0], "warehouseAddressCode") != "WH2604283535967233" {
		t.Fatalf("zero-stock ARP east warehouse was not removed: %#v", filtered)
	}
}

func TestAutomaticOMSAccountUsesXLWMSDecision(t *testing.T) {
	decision := json.RawMessage(`{
		"account_decision":{"account_key":"OMS_US_1","configured":true,"requires_manual":false}
	}`)
	account, err := automaticOMSAccount(decision)
	if err != nil || account != "oms_us_1" {
		t.Fatalf("account decision = (%q, %v)", account, err)
	}
	manual := json.RawMessage(`{
		"account_decision":{"configured":false,"requires_manual":true,"reason":"SKU 未配置账户"}
	}`)
	if _, err := automaticOMSAccount(manual); err == nil || err.Error() != "SKU 未配置账户" {
		t.Fatalf("manual account decision error = %v", err)
	}
}

func TestAutomaticInventoryWarehouseKeysRejectsManualInventoryDecision(t *testing.T) {
	decision := json.RawMessage(`{
		"complete":true,
		"package_resolution":{"complete":true},
		"records":[{"sku":"VH-20pcs-Pink-45cm","requires_manual":true,
			"reason":"美东和美西正品产品可用库存合计0，保留库存并转人工处理",
			"regions":[{"warehouses":[
				{"warehouse_key":"DPS002","active":true,"query_status":"succeeded","available_amount":0,"selectable":false},
				{"warehouse_key":"ARP_EAST","active":true,"query_status":"succeeded","available_amount":0,"selectable":false}
			]}]
		}]
	}`)
	_, err := automaticInventoryWarehouseKeys(decision, map[string]int{"VH-20pcs-Pink-45cm": 1})
	if err == nil || err.Error() != "美东和美西正品产品可用库存合计0，保留库存并转人工处理" {
		t.Fatalf("manual inventory decision must block automatic fulfillment: %v", err)
	}
}

func TestAutomaticInventoryCheckPersistsManualInventoryDecision(t *testing.T) {
	decision := json.RawMessage(`{
		"complete":true,
		"package_resolution":{"complete":true},
		"records":[{"sku":"SKU-1","requires_manual":true,"reason":"可用库存不足，需要人工发货"}]
	}`)
	check := automaticInventoryCheck(decision, map[string]int{"SKU-1": 1}, nil)
	if check.Status != "manual" {
		t.Fatalf("inventory check status = %q, want manual", check.Status)
	}
	if len(check.Categories) != 1 || check.Categories[0] != "inventory_rule" {
		t.Fatalf("inventory check categories = %#v", check.Categories)
	}
	if len(check.ReasonDetails) != 1 || check.ReasonDetails[0] != "可用库存不足，需要人工发货" {
		t.Fatalf("inventory check reasons = %#v", check.ReasonDetails)
	}
}

func TestLowestQuotedChannelRejectsMixedCurrencies(t *testing.T) {
	_, err := lowestQuotedChannel([]quotedChannel{
		{Candidate: shein.ShippingQuoteCandidate{PerformanceCost: "1", CurrencyCode: "USD"}},
		{Candidate: shein.ShippingQuoteCandidate{PerformanceCost: "1", CurrencyCode: "EUR"}},
	})
	if err == nil {
		t.Fatal("mixed-currency quote was accepted")
	}
}

func TestSelectAutomaticQuotedChannelPicksLowestPrice(t *testing.T) {
	rules := currentSheinRules("ARP_EAST")
	quotes := []quotedChannel{
		{
			Quote:     shein.ShippingQuote{WarehouseAddressCode: "WH2607084039788546"},
			Candidate: shein.ShippingQuoteCandidate{ExpressChannelCode: "SPEEDX-US", ExpressShortName: "SpeedX", PerformanceCost: "10.00", CurrencyCode: "USD"},
			Priority:  3,
			Rules:     rules,
		},
		{
			Quote:     shein.ShippingQuote{WarehouseAddressCode: "WH2607084039788546"},
			Candidate: shein.ShippingQuoteCandidate{ExpressChannelCode: "GOFO-D2D250718-Na", ExpressShortName: "GOFO", PerformanceCost: "10.40", CurrencyCode: "USD"},
			Priority:  1,
			Rules:     rules,
		},
	}
	selected, reason, err := selectAutomaticQuotedChannel(quotes)
	if err != nil {
		t.Fatalf("selectAutomaticQuotedChannel returned error: %v", err)
	}
	if selected.Candidate.ExpressChannelCode != "SPEEDX-US" {
		t.Fatalf("expected cheapest SPEEDX, got %#v", selected)
	}
	if reason != "按 XLWMS ARP_EAST 基础规则选择最低运费 SPEEDX" {
		t.Fatalf("unexpected selection reason: %q", reason)
	}
}

func TestSelectAutomaticQuotedChannelPrefersARPWhenPricesTie(t *testing.T) {
	quotes := []quotedChannel{
		{
			Quote:     shein.ShippingQuote{WarehouseAddressCode: "WH2604283535967233"},
			Candidate: shein.ShippingQuoteCandidate{ExpressChannelCode: "GOFO-DPS", PerformanceCost: "10.00", CurrencyCode: "USD"},
			Rules:     currentSheinRules("DPS002"),
		},
		{
			Quote:     shein.ShippingQuote{WarehouseAddressCode: "WH2607084039788546"},
			Candidate: shein.ShippingQuoteCandidate{ExpressChannelCode: "GOFO-ARP", PerformanceCost: "10.00", CurrencyCode: "USD"},
			Rules:     currentSheinRules("ARP_EAST"),
		},
	}
	selected, reason, err := selectAutomaticQuotedChannel(quotes)
	if err != nil {
		t.Fatalf("selectAutomaticQuotedChannel returned error: %v", err)
	}
	if selected.Quote.WarehouseAddressCode != "WH2607084039788546" {
		t.Fatalf("same price must prefer ARP, got %#v", selected)
	}
	if reason != "按 XLWMS ARP_EAST 基础规则选择最低运费 GOFO" {
		t.Fatalf("unexpected selection reason: %q", reason)
	}
}

func TestSelectAutomaticQuotedChannelUsesConfiguredPriorityWithinDelta(t *testing.T) {
	rules := currentSheinRules("ARP_EAST")
	rules.SelectionMode = "carrier_priority_within_delta"
	rules.MaxPriceDelta = 0.5
	quotes := []quotedChannel{
		{Quote: shein.ShippingQuote{WarehouseAddressCode: "WH2607084039788546"}, Candidate: shein.ShippingQuoteCandidate{ExpressChannelCode: "SPEEDX-US", PerformanceCost: "10.00", CurrencyCode: "USD"}, Priority: 3, Rules: rules},
		{Quote: shein.ShippingQuote{WarehouseAddressCode: "WH2607084039788546"}, Candidate: shein.ShippingQuoteCandidate{ExpressChannelCode: "GOFO-US", PerformanceCost: "10.40", CurrencyCode: "USD"}, Priority: 1, Rules: rules},
	}
	selected, _, err := selectAutomaticQuotedChannel(quotes)
	if err != nil {
		t.Fatal(err)
	}
	if shein.CarrierCode(selected.Candidate.ExpressChannelCode) != "GOFO" {
		t.Fatalf("configured priority was not applied: %#v", selected)
	}
}

func currentSheinRules(warehouseKey string) shein.WarehouseCarrierRules {
	tiePriority := 2
	if strings.HasPrefix(warehouseKey, "ARP") {
		tiePriority = 1
	}
	return shein.WarehouseCarrierRules{WarehouseKey: warehouseKey, SelectionMode: "lowest_price", WarehouseTiePriority: tiePriority}
}

func TestCarrierCoverageReasonRecognizesGOFOPostalCodeRejection(t *testing.T) {
	reason := "Please check whether the package delivery information is correct. Error reason: This ZIP code is not supported by GOFO delivery service."
	if !carrierCoverageReason(reason, "GOFO-D2D250718-Na") {
		t.Fatal("GOFO ZIP coverage rejection must trigger a carrier fallback")
	}
	if carrierCoverageReason("The ZIP code is invalid", "GOFO-D2D250718-Na") {
		t.Fatal("an invalid address must not be treated as a carrier-specific coverage failure")
	}
}

func TestCarrierCoverageErrorFromPreservesReasonAndCarrier(t *testing.T) {
	reason := "This postal code is not supported by GOFO delivery service"
	err := carrierCoverageErrorFrom(&shein.APIError{Code: "400", Message: reason}, "GOFO-D2D250718-Na")
	if err == nil {
		t.Fatal("expected a recoverable carrier coverage error")
	}
	if err.Carrier != "GOFO" || err.Reason != reason {
		t.Fatalf("unexpected coverage error: %#v", err)
	}
	if got := carrierCoverageErrorFrom(errors.New(reason), "GOFO-D2D250718-Na"); got != nil {
		t.Fatal("local workflow errors must not be mistaken for a SHEIN carrier rejection")
	}
}

func TestAutomaticCarrierKeyUsesCarrierFamily(t *testing.T) {
	if got := automaticCarrierKey("GOFO-D2D250718-Na"); got != "GOFO" {
		t.Fatalf("automaticCarrierKey = %q, want GOFO", got)
	}
}

func TestCompleteAutomaticWarehouseHandoffCreatesDPSParcelOnly(t *testing.T) {
	server := &Server{}
	if err := server.completeAutomaticWarehouseHandoff(nil, autoQueueRef{ShopKey: "beauty-hangers-home", OrderNo: "GSU-ARP-1"}, shein.AutoFulfillmentJob{
		WarehouseAddressCode: "WH2607084039788546",
	}); err != nil {
		t.Fatalf("ARP handoff must wait for automatic warehouse assignment: %v", err)
	}
	if err := server.completeAutomaticWarehouseHandoff(nil, autoQueueRef{ShopKey: "beauty-hangers-home", OrderNo: "GSU-DPS-1"}, shein.AutoFulfillmentJob{
		WarehouseAddressCode: "WH2604283535967233",
	}); err == nil || err.Error() != "领星查询服务未配置" {
		t.Fatalf("DPS handoff must create a Lingxing parcel after the label: %v", err)
	}
	if err := server.completeAutomaticWarehouseHandoff(nil, autoQueueRef{ShopKey: "other-shop", OrderNo: "GSU-DPS-2"}, shein.AutoFulfillmentJob{
		WarehouseAddressCode: "WH2604283535967233",
	}); err != nil {
		t.Fatalf("unconfigured shops must not auto-create DPS parcels: %v", err)
	}
}
