package shein

import "testing"

func TestClassifyOrderQueueItemUsesSKUCodeMapping(t *testing.T) {
	item := OrderQueueItem{
		Detail: map[string]any{
			"optionalLogisticsList": []any{float64(1)},
			"printOrderStatus":      float64(1),
			"orderGoodsInfoList": []any{map[string]any{
				"goodsId": "GOODS-1", "skuCode": "SKU-CODE", "sellerSku": "SELLER-SKU",
			}},
		},
	}
	item.Goods = queueGoods(item.Detail)
	item.ItemCount = len(item.Goods)
	classifyOrderQueueItem(&item, map[string]packageMapping{
		"SKU-CODE": {
			SheinSKU: "SKU-CODE", WarehouseSKU: "WH-1", WarehouseQty: "2", MappingCount: 1,
			Spec: PackageSpec{LengthCM: "20", WidthCM: "15", HeightCM: "5", WeightKG: "0.3"},
		},
	})
	if !item.AutoEligible {
		t.Fatalf("single item should be eligible, reasons=%v", item.ManualReasons)
	}
	if item.SheinSKU != "SKU-CODE" || item.WarehouseSKU != "WH-1" {
		t.Fatalf("classification used wrong SKU mapping: %#v", item)
	}
	if item.Goods[0].WarehouseSKU != "WH-1" || item.Goods[0].WarehouseQuantity != "2" {
		t.Fatalf("goods line did not expose warehouse mapping: %#v", item.Goods[0])
	}
}

func TestClassifyOrderQueueItemRoutesMultiItemToManual(t *testing.T) {
	item := OrderQueueItem{
		Detail: map[string]any{
			"optionalLogisticsList": []any{float64(1)},
			"orderGoodsInfoList": []any{
				map[string]any{"skuCode": "SKU-1"},
				map[string]any{"skuCode": "SKU-2"},
			},
		},
	}
	item.Goods = queueGoods(item.Detail)
	item.ItemCount = len(item.Goods)
	classifyOrderQueueItem(&item, nil)
	if item.AutoEligible || len(item.ManualReasons) == 0 {
		t.Fatalf("multi-item order was not routed to manual review: %#v", item)
	}
}

func TestClassifyOrderQueueItemRoutesSingleSKUQuantityGreaterThanOneToManual(t *testing.T) {
	item := OrderQueueItem{
		Detail: map[string]any{
			"optionalLogisticsList": []any{float64(1)},
			"printOrderStatus":      float64(1),
			"orderGoodsInfoList": []any{map[string]any{
				"skuCode": "SKU-CODE", "quantity": float64(2),
			}},
		},
	}
	item.Goods = queueGoods(item.Detail)
	item.ItemCount = len(item.Goods)
	classifyOrderQueueItem(&item, map[string]packageMapping{
		"SKU-CODE": {
			SheinSKU: "SKU-CODE", WarehouseSKU: "WH-1", WarehouseQty: "2", MappingCount: 1,
			Spec: PackageSpec{LengthCM: "20", WidthCM: "15", HeightCM: "5", WeightKG: "0.3"},
		},
	})
	if item.AutoEligible {
		t.Fatalf("single SKU with quantity 2 must go to manual review: %#v", item)
	}
	if item.Goods[0].Quantity != 2 {
		t.Fatalf("goods quantity = %d, want 2", item.Goods[0].Quantity)
	}
	found := false
	for _, reason := range item.ManualReasons {
		if reason == "多件订单需人工确认包裹" {
			found = true
		}
	}
	if !found {
		t.Fatalf("quantity>1 was not classified as multi-item: %#v", item.ManualReasons)
	}
}

func TestOptionalLogisticsListControlsPlatformLabelPurchase(t *testing.T) {
	integratedPending := map[string]any{
		"optionalLogisticsList": []any{float64(1)},
		"performanceType":       float64(2),
	}
	if !CanPurchasePlatformLabel(integratedPending) {
		t.Fatal("platform-logistics order must buy a label without export-address")
	}
	if RequiresAddressTransition(integratedPending, "1") {
		t.Fatal("pending integrated order must not call handleType=2")
	}
	selfShipOnly := map[string]any{"optionalLogisticsList": []any{float64(2)}}
	if CanPurchasePlatformLabel(selfShipOnly) {
		t.Fatal("self-ship-only order must not enter platform label purchase")
	}
	if !RequiresAddressTransition(selfShipOnly, "1") {
		t.Fatal("pending self-ship order still needs export-address")
	}
	if RequiresAddressTransition(selfShipOnly, "2") {
		t.Fatal("already pending-shipment orders do not transition again")
	}
	if IsCODOrder(map[string]any{"isCod": 2}) || !IsCODOrder(map[string]any{"isCod": 1}) {
		t.Fatal("COD detection must treat isCod=1 as cash on delivery")
	}
}

func TestOrderFulfilledOnPlatformArchivesShippedAndDelivered(t *testing.T) {
	if !OrderFulfilledOnPlatform("5") || !OrderFulfilledOnPlatform("delivered") || !OrderFulfilledOnPlatform("4") {
		t.Fatal("SHEIN shipped/delivered statuses must count as already fulfilled")
	}
	if OrderFulfilledOnPlatform("1") || OrderFulfilledOnPlatform("2") || OrderFulfilledOnPlatform("6") || OrderFulfilledOnPlatform("7") {
		t.Fatal("open or refunded SHEIN statuses must not auto-archive warehouse watch")
	}
	if NormalizeOrderStatus("7") != "pending_pickup" {
		t.Fatal("SHEIN status 7 is pending pickup, not shipped")
	}
	if LabelPrintable(FulfillmentTask{OrderStatusNormalized: "delivered"}) {
		t.Fatal("delivered SHEIN orders must not stay printable")
	}
	if !LabelPrintable(FulfillmentTask{OrderStatusNormalized: "pending_shipping"}) {
		t.Fatal("open SHEIN orders must stay printable")
	}
}

func TestPackageSpecRequiresEveryPositiveValue(t *testing.T) {
	complete := PackageSpec{LengthCM: "20", WidthCM: "15", HeightCM: "5", WeightKG: "0.3"}
	if !complete.Complete() {
		t.Fatal("complete package spec was rejected")
	}
	complete.WeightKG = "0"
	if complete.Complete() {
		t.Fatal("zero package weight was accepted")
	}
}
