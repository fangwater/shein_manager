package shein

import (
	"testing"
	"time"

	"shein-api-manager/internal/xlwms"
)

func TestClassifyOrderQueueItemPrefersSellerSKUMapping(t *testing.T) {
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
		"seller:SELLER-SKU": {
			SheinSKU: "SELLER-SKU", WarehouseSKU: "WH-1", WarehouseQty: "2", MappingCount: 1,
			Spec:  PackageSpec{LengthCM: "20", WidthCM: "15", HeightCM: "5", WeightKG: "0.3"},
			Items: []WarehouseMappingItem{{WarehouseSKU: "WH-1", Quantity: 2, Spec: PackageSpec{LengthCM: "20", WidthCM: "15", HeightCM: "5", WeightKG: "0.3"}}},
		},
		"code:SKU-CODE": {SheinSKU: "SKU-CODE", WarehouseSKU: "WRONG", WarehouseQty: "1", MappingCount: 1},
	})
	if !item.AutoEligible {
		t.Fatalf("single item should be eligible, reasons=%v", item.ManualReasons)
	}
	if item.SheinSKU != "SELLER-SKU" || item.WarehouseSKU != "WH-1" {
		t.Fatalf("classification used wrong SKU mapping: %#v", item)
	}
	if item.Goods[0].WarehouseSKU != "WH-1" || item.Goods[0].WarehouseQuantity != "2" {
		t.Fatalf("goods line did not expose warehouse mapping: %#v", item.Goods[0])
	}
}

func TestPlatformSKUCandidatesUseCodeAsFinalFallback(t *testing.T) {
	aliases := map[string][]string{"SKU-CODE": {"ALIAS-01", "ALIAS-01"}}
	if got := platformSKUCandidates(QueueGoods{SKUCode: " SKU-CODE "}, aliases); len(got) != 2 || got[0] != "ALIAS-01" || got[1] != "SKU-CODE" {
		t.Fatalf("alias candidates = %#v", got)
	}
	if got := platformSKUCandidates(QueueGoods{SKUCode: "SKU-CODE", SellerSKU: " SELLER-01 "}, aliases); len(got) != 2 || got[0] != "SELLER-01" || got[1] != "SKU-CODE" {
		t.Fatalf("seller candidates = %#v", got)
	}
}

func TestInventoryCheckMovesOtherwiseEligibleOrderToManual(t *testing.T) {
	checkedAt := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	item := OrderQueueItem{
		AutoEligible:       true,
		staticAutoEligible: true,
		DetailFetchedAt:    checkedAt,
		InventoryCheck: &InventoryCheck{
			SourceDetailFetchedAt: checkedAt,
			Status:                "manual",
			ReasonDetails:         []string{"库存低于自动发货安全线"},
		},
	}
	applyInventoryCheck(&item)
	if item.AutoEligible || item.ReadyForAutomaticFulfillment() {
		t.Fatalf("manual inventory check must block automatic fulfillment: %#v", item)
	}
	if len(item.ManualReasons) != 1 || item.ManualReasons[0] != "库存低于自动发货安全线" {
		t.Fatalf("manual reasons = %#v", item.ManualReasons)
	}
}

func TestEligibleInventoryCheckRequiresCurrentOrderDetail(t *testing.T) {
	checkedAt := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	item := OrderQueueItem{
		AutoEligible:       true,
		staticAutoEligible: true,
		DetailFetchedAt:    checkedAt,
		InventoryCheck: &InventoryCheck{
			SourceDetailFetchedAt: checkedAt.Add(-time.Second),
			Status:                "eligible",
		},
	}
	if item.ReadyForAutomaticFulfillment() {
		t.Fatal("stale inventory check must not permit automatic fulfillment")
	}
	item.InventoryCheck.SourceDetailFetchedAt = checkedAt
	if !item.ReadyForAutomaticFulfillment() {
		t.Fatal("current eligible inventory check should permit automatic fulfillment")
	}
}

func TestCanRunAutomaticFulfillmentIncludesFailedJob(t *testing.T) {
	checkedAt := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	item := OrderQueueItem{
		AutoEligible:    true,
		DetailFetchedAt: checkedAt,
		InventoryCheck: &InventoryCheck{
			SourceDetailFetchedAt: checkedAt,
			Status:                "eligible",
		},
		Job: &AutoFulfillmentJob{Status: "failed"},
	}
	if !item.CanRunAutomaticFulfillment() {
		t.Fatal("failed automatic job must remain runnable in the pending queue")
	}
	item.Job.Status = "running"
	if item.CanRunAutomaticFulfillment() {
		t.Fatal("active automatic job must not be duplicated in the pending queue")
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
		"code:SKU-CODE": {
			SheinSKU: "SKU-CODE", WarehouseSKU: "WH-1", WarehouseQty: "2", MappingCount: 1,
			Spec:  PackageSpec{LengthCM: "20", WidthCM: "15", HeightCM: "5", WeightKG: "0.3"},
			Items: []WarehouseMappingItem{{WarehouseSKU: "WH-1", Quantity: 2, Spec: PackageSpec{LengthCM: "20", WidthCM: "15", HeightCM: "5", WeightKG: "0.3"}}},
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

func TestPackageMappingFromXLWMSPreservesRecipeAndSpec(t *testing.T) {
	length, width, height, weight := 20.0, 15.0, 5.0, 0.3
	mapping := packageMappingFromXLWMS(xlwms.PlatformSKUMapping{
		PlatformSKU: "SELLER-01",
		Items: []xlwms.PlatformSKUMappingItem{
			{WarehouseSKU: "WH-A", Quantity: 2, LengthCM: &length, WidthCM: &width, HeightCM: &height, WeightKG: &weight},
		},
	})
	if mapping.MappingCount != 1 || mapping.WarehouseSKU != "WH-A" || mapping.WarehouseQty != "2" || !mapping.Spec.Complete() {
		t.Fatalf("unexpected mapping: %#v", mapping)
	}
}

func TestClassifyOrderQueueItemRoutesCombinationMappingToManual(t *testing.T) {
	item := OrderQueueItem{
		Detail: map[string]any{"optionalLogisticsList": []any{float64(1)}, "printOrderStatus": float64(1)},
		Goods:  []QueueGoods{{SKUCode: "CODE", SellerSKU: "SELLER-01", Quantity: 1}}, ItemCount: 1,
	}
	classifyOrderQueueItem(&item, map[string]packageMapping{
		"seller:SELLER-01": {SheinSKU: "SELLER-01", MappingCount: 1, Items: []WarehouseMappingItem{{WarehouseSKU: "WH-A", Quantity: 1}, {WarehouseSKU: "WH-B", Quantity: 1}}},
	})
	if item.AutoEligible || len(item.Goods[0].WarehouseItems) != 2 {
		t.Fatalf("combination recipe was not preserved for manual fulfillment: %#v", item)
	}
}

func TestOrderSKUAliasesIgnoresMissingAndDuplicateValues(t *testing.T) {
	aliases := orderSKUAliases(map[string]any{"orderGoodsInfoList": []any{
		map[string]any{"skuCode": "CODE-1", "sellerSku": "SELLER-01"},
		map[string]any{"skuCode": "CODE-1", "sellerSku": "SELLER-01"},
		map[string]any{"skuCode": "CODE-2", "sellerSku": ""},
	}})
	if len(aliases) != 1 || aliases[0] != [2]string{"CODE-1", "SELLER-01"} {
		t.Fatalf("aliases = %#v", aliases)
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
