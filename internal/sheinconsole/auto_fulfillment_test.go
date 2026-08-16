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
