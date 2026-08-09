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
