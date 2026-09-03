package sheinconsole

import (
	"encoding/json"
	"testing"
)

func TestFulfillmentTasksFromOrderDetail(t *testing.T) {
	result := map[string]any{"info": []any{map[string]any{
		"orderNo":        "ORDER-1",
		"orderPlaceType": json.Number("1"),
		"packageWaybillList": []any{map[string]any{
			"packageNo":        "PACKAGE-1",
			"deliveryNo":       "DELIVERY-1",
			"waybillNo":        "WAYBILL-1",
			"carrierCode":      "CARRIER-1",
			"printOrderStatus": json.Number("2"),
		}},
	}}}

	tasks := fulfillmentTasksFromOrderDetail(result)
	if len(tasks) != 1 {
		t.Fatalf("tasks length = %d, want 1", len(tasks))
	}
	task := tasks[0]
	if task.OrderNo != "ORDER-1" || task.PackageNo != "PACKAGE-1" || task.DeliveryNo != "DELIVERY-1" {
		t.Fatalf("unexpected task identifiers: %#v", task)
	}
	if task.OrderPlaceType == nil || *task.OrderPlaceType != 1 {
		t.Fatalf("order place type = %#v, want 1", task.OrderPlaceType)
	}
	if task.PrintStatus == nil || *task.PrintStatus != 2 || task.Status != "label_ready" {
		t.Fatalf("unexpected print state: %#v", task)
	}
}

func TestFulfillmentTasksFromOrderDetailKeepShippedParcelsSearchable(t *testing.T) {
	result := map[string]any{"info": []any{map[string]any{
		"orderNo":        "GSU1R643R00MP5M",
		"orderPlaceType": json.Number("2"),
		"packageWaybillList": []any{map[string]any{
			"packageNo":        "GU2608134518147081",
			"deliveryNo":       "GU2608134518147081",
			"waybillNo":        "GFUS01067727511041",
			"carrierCode":      "GOFO-D2D250718-Na",
			"printOrderStatus": json.Number("1"),
		}},
	}}}
	tasks := fulfillmentTasksFromOrderDetail(result)
	if len(tasks) != 1 || tasks[0].Status != "ready" {
		t.Fatalf("shipped parcel must stay visible as ready, got %#v", tasks)
	}
}

func TestFulfillmentTaskFromPlace(t *testing.T) {
	request := map[string]any{
		"expressChannelCode": "CHANNEL-1",
		"packageInfoList":    []any{map[string]any{"orderNo": "ORDER-1"}},
	}
	result := map[string]any{"info": map[string]any{
		"placeRequestId": "PLACE-1",
		"deliveryNo":     "DELIVERY-1",
	}}

	task, ok := fulfillmentTaskFromPlace(request, result)
	if !ok {
		t.Fatal("fulfillmentTaskFromPlace did not produce a task")
	}
	if task.OrderNo != "ORDER-1" || task.PlaceRequestID != "PLACE-1" || task.DeliveryNo != "DELIVERY-1" {
		t.Fatalf("unexpected task: %#v", task)
	}
	if task.OrderPlaceType == nil || *task.OrderPlaceType != 2 || task.Status != "placed" {
		t.Fatalf("unexpected task type/status: %#v", task)
	}
}

func TestFulfillmentResultFromCheckUsesOfficialFields(t *testing.T) {
	request := map[string]any{"deliveryNo": "DELIVERY-1"}
	result := map[string]any{"info": map[string]any{
		"placeRequestId":           "PLACE-1",
		"deliveryNo":               "DELIVERY-1",
		"expressChannelCode":       "CHANNEL-1",
		"warehouseAddressCode":     "WAREHOUSE-1",
		"handleResult":             json.Number("2"),
		"printStatus":              json.Number("1"),
		"placeStateFailReasonDesc": "",
	}}

	check := fulfillmentResultFromCheck(request, result)
	if check.PlaceRequestID != "PLACE-1" || check.DeliveryNo != "DELIVERY-1" {
		t.Fatalf("unexpected result identifiers: %#v", check)
	}
	if check.HandleResult == nil || *check.HandleResult != 2 || check.Status != "ready" {
		t.Fatalf("unexpected result state: %#v", check)
	}
	if check.PrintStatus == nil || *check.PrintStatus != 1 {
		t.Fatalf("unexpected print status: %#v", check.PrintStatus)
	}
}

func TestFulfillmentResultFromCheckRecognizesFailureAndPrinted(t *testing.T) {
	failed := fulfillmentResultFromCheck(nil, map[string]any{"info": map[string]any{
		"deliveryNo": "DELIVERY-1", "handleResult": float64(3), "placeStateFailReasonDesc": "rejected",
	}})
	if failed.Status != "failed" || failed.FailureReason != "rejected" {
		t.Fatalf("unexpected failed result: %#v", failed)
	}

	printed := fulfillmentResultFromCheck(nil, map[string]any{"info": map[string]any{
		"deliveryNo": "DELIVERY-1", "handleResult": float64(2), "printStatus": float64(2),
	}})
	if printed.Status != "label_ready" {
		t.Fatalf("printed result status = %q", printed.Status)
	}
}

func TestFulfillmentLabelIdentifiers(t *testing.T) {
	deliveryNo, orderNo, packageNos := fulfillmentLabelIdentifiers(map[string]any{
		"orderNo": "ORDER-1", "packageNo": []any{"PACKAGE-1", "PACKAGE-2"},
	})
	if deliveryNo != "" || orderNo != "ORDER-1" {
		t.Fatalf("unexpected label identifiers: %q %q", deliveryNo, orderNo)
	}
	if len(packageNos) != 2 || packageNos[1] != "PACKAGE-2" {
		t.Fatalf("package numbers = %#v", packageNos)
	}
}

func TestShippingQuoteFromChannels(t *testing.T) {
	request := map[string]any{
		"orderNo": "ORDER-1", "warehouseAddressCode": "WAREHOUSE-1",
	}
	result := map[string]any{"info": map[string]any{
		"preRequestId": "QUOTE-1",
		"channelInfoList": []any{
			map[string]any{
				"expressId": 995.0, "expressIdCode": "Carrier A",
				"expressChannelCode": "CHANNEL-A", "expressShortName": "A",
				"performanceCost": 13.02, "currencyCode": "USD",
				"estimateMinDay": 2.0, "estimateMaxDay": 4.0,
			},
			map[string]any{
				"expressId": 1730.0, "expressChannelCode": "CHANNEL-B",
				"performanceCost": 12.5, "currencyCode": "USD",
			},
		},
	}}

	quote, ok := shippingQuoteFromChannels(request, result)
	if !ok {
		t.Fatal("shippingQuoteFromChannels did not produce a quote")
	}
	if quote.PreRequestID != "QUOTE-1" || quote.OrderNo != "ORDER-1" || quote.WarehouseAddressCode != "WAREHOUSE-1" {
		t.Fatalf("unexpected quote: %#v", quote)
	}
	if len(quote.Candidates) != 2 || quote.Candidates[0].PerformanceCost != "13.02" {
		t.Fatalf("unexpected candidates: %#v", quote.Candidates)
	}
	if quote.Candidates[0].ExpressID == nil || *quote.Candidates[0].ExpressID != 995 {
		t.Fatalf("unexpected express id: %#v", quote.Candidates[0].ExpressID)
	}
}

func TestShippingQuoteFromChannelsSkipsDisabledPolicyChannels(t *testing.T) {
	request := map[string]any{"orderNo": "ORDER-1", "warehouseAddressCode": "WH2604283535967233"}
	result := map[string]any{"info": map[string]any{
		"preRequestId": "QUOTE-1",
		"channelInfoList": []any{
			map[string]any{
				"expressChannelCode": "GOFO-D2D250718-Na", "expressShortName": "GOFO",
				"performanceCost": 9.1, "currencyCode": "USD",
				"availableStatus": "0", "unavailableReason": "SHEIN 发货策略已在 DPS002 仓库禁用 GOFO",
			},
			map[string]any{
				"expressChannelCode": "UPS-GROUND", "expressShortName": "UPS",
				"performanceCost": 10.2, "currencyCode": "USD",
			},
		},
	}}
	quote, ok := shippingQuoteFromChannels(request, result)
	if !ok {
		t.Fatal("shippingQuoteFromChannels should keep the enabled channel")
	}
	if len(quote.Candidates) != 1 || quote.Candidates[0].ExpressChannelCode != "UPS-GROUND" {
		t.Fatalf("disabled policy channel was stored: %#v", quote.Candidates)
	}
}
