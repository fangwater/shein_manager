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
	request, err := channelRequest("ORDER-1", "WAREHOUSE-1", shein.PackageSpec{
		LengthCM: "20", WidthCM: "15", HeightCM: "5", WeightKG: "0.3",
	}, []shein.QueueGoods{{GoodsID: "GOODS-1"}})
	if err != nil {
		t.Fatalf("channelRequest returned error: %v", err)
	}
	weight := request["packageWeightInfo"].(map[string]any)
	if weight["packageWeight"] != "300" {
		t.Fatalf("package weight = %v, want 300g", weight["packageWeight"])
	}
	prePackage := request["prePackageInfo"].(map[string]any)
	if len(prePackage["goodsIds"].([]any)) != 1 {
		t.Fatalf("goods IDs missing from channel request: %#v", request)
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

func TestLowestQuotedChannelRejectsMixedCurrencies(t *testing.T) {
	_, err := lowestQuotedChannel([]quotedChannel{
		{Candidate: shein.ShippingQuoteCandidate{PerformanceCost: "1", CurrencyCode: "USD"}},
		{Candidate: shein.ShippingQuoteCandidate{PerformanceCost: "1", CurrencyCode: "EUR"}},
	})
	if err == nil {
		t.Fatal("mixed-currency quote was accepted")
	}
}
