package sheinconsole

import (
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
	quotes := []quotedChannel{
		{
			Quote:     shein.ShippingQuote{WarehouseAddressCode: "WH2607084039788546"},
			Candidate: shein.ShippingQuoteCandidate{ExpressChannelCode: "SPEEDX-US", ExpressShortName: "SpeedX", PerformanceCost: "10.00", CurrencyCode: "USD"},
			Priority:  3,
		},
		{
			Quote:     shein.ShippingQuote{WarehouseAddressCode: "WH2607084039788546"},
			Candidate: shein.ShippingQuoteCandidate{ExpressChannelCode: "GOFO-D2D250718-Na", ExpressShortName: "GOFO", PerformanceCost: "10.40", CurrencyCode: "USD"},
			Priority:  1,
		},
	}
	selected, reason, err := selectAutomaticQuotedChannel(quotes)
	if err != nil {
		t.Fatalf("selectAutomaticQuotedChannel returned error: %v", err)
	}
	if selected.Candidate.ExpressChannelCode != "SPEEDX-US" {
		t.Fatalf("expected cheapest SPEEDX, got %#v", selected)
	}
	if reason != "选择最低运费 SPEEDX / ARP_EAST" {
		t.Fatalf("unexpected selection reason: %q", reason)
	}
}

func TestSelectAutomaticQuotedChannelPrefersARPWhenPricesTie(t *testing.T) {
	quotes := []quotedChannel{
		{
			Quote:     shein.ShippingQuote{WarehouseAddressCode: "WH2604283535967233"},
			Candidate: shein.ShippingQuoteCandidate{ExpressChannelCode: "GOFO-DPS", PerformanceCost: "10.00", CurrencyCode: "USD"},
		},
		{
			Quote:     shein.ShippingQuote{WarehouseAddressCode: "WH2607084039788546"},
			Candidate: shein.ShippingQuoteCandidate{ExpressChannelCode: "GOFO-ARP", PerformanceCost: "10.00", CurrencyCode: "USD"},
		},
	}
	selected, reason, err := selectAutomaticQuotedChannel(quotes)
	if err != nil {
		t.Fatalf("selectAutomaticQuotedChannel returned error: %v", err)
	}
	if selected.Quote.WarehouseAddressCode != "WH2607084039788546" {
		t.Fatalf("same price must prefer ARP, got %#v", selected)
	}
	if reason != "选择最低运费 GOFO / ARP_EAST" {
		t.Fatalf("unexpected selection reason: %q", reason)
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
