package shein

import "testing"

func TestEndpointsCoverRequestedSHEINAPIs(t *testing.T) {
	want := map[string]string{
		"order-list":                   OrderListPath,
		"order-detail":                 OrderDetailPath,
		"export-address":               ExportAddressPath,
		"available-shipping-warehouse": AvailableShippingWarehousePath,
		"order-mapping-channels":       OrderMappingChannelsPath,
		"place-express-order":          PlaceExpressOrderPath,
		"check-express-order":          CheckExpressOrderPath,
		"print-express-info":           PrintExpressInfoPath,
		"logistics-track":              LogisticsTrackPath,
	}
	if len(Endpoints) != len(want) {
		t.Fatalf("endpoint count = %d, want %d", len(Endpoints), len(want))
	}
	for operation, path := range want {
		if Endpoints[operation].Path != path {
			t.Errorf("%s path = %q, want %q", operation, Endpoints[operation].Path, path)
		}
	}
}

func TestOrderListValidation(t *testing.T) {
	valid := map[string]any{
		"queryType": 2, "startTime": "2026-08-02 10:00:00", "endTime": "2026-08-02 12:00:00",
		"page": 1, "pageSize": 30,
	}
	if err := Validate("order-list", valid); err != nil {
		t.Fatal(err)
	}
	tooLarge := clone(valid)
	tooLarge["pageSize"] = 31
	if err := Validate("order-list", tooLarge); err == nil {
		t.Fatal("pageSize above 30 must fail")
	}
	tooWide := clone(valid)
	tooWide["endTime"] = "2026-08-04 10:00:01"
	if err := Validate("order-list", tooWide); err == nil {
		t.Fatal("window above 48 hours must fail")
	}
}

func TestOrderDetailAndShippingValidation(t *testing.T) {
	if err := Validate("order-detail", map[string]any{"orderNoList": []any{"ORDER-1"}}); err != nil {
		t.Fatal(err)
	}
	orders := make([]any, 31)
	for index := range orders {
		orders[index] = "ORDER"
	}
	if err := Validate("order-detail", map[string]any{"orderNoList": orders}); err == nil {
		t.Fatal("more than 30 order numbers must fail")
	}
	channels := map[string]any{
		"orderNo": "ORDER-1", "warehouseAddressCode": "WH2604283535967233",
		"packageSizeInfo":   map[string]any{"packageLength": "10", "packageWidth": "8", "packageHeight": "2", "unit": "cm"},
		"packageWeightInfo": map[string]any{"packageWeight": "200.5", "unit": "g"},
	}
	if err := Validate("order-mapping-channels", channels); err != nil {
		t.Fatal(err)
	}
	named := clone(channels)
	named["warehouseAddressCode"] = "WH-UNKNOWN"
	named["warehouseName"] = "ARP仓-美东"
	if err := Validate("order-mapping-channels", named); err != nil {
		t.Fatal(err)
	}
	if got := outboundRequestData("order-mapping-channels", named); got["warehouseName"] != nil || got["warehouseAddressCode"] != "WH-UNKNOWN" {
		t.Fatalf("warehouseName must stay local: %#v", got)
	}
	withGoods := clone(channels)
	withGoods["prePackageInfo"] = map[string]any{"goodsIds": []any{"19575668618"}}
	if got := outboundRequestData("order-mapping-channels", withGoods); got["prePackageInfo"] != nil {
		t.Fatalf("non-COD channel request must drop goods details: %#v", got)
	}
	cod := clone(withGoods)
	cod["isCod"] = 1
	if got := outboundRequestData("order-mapping-channels", cod); got["prePackageInfo"] == nil || got["isCod"] != nil {
		t.Fatalf("COD channel request must keep goods details locally only: %#v", got)
	}
	blocked := clone(channels)
	blocked["warehouseAddressCode"] = "WH2602103441974274"
	if err := Validate("order-mapping-channels", blocked); err == nil {
		t.Fatal("PG warehouses must not be quoted")
	}
	unknown := clone(channels)
	unknown["warehouseAddressCode"] = "WH2602103360052227"
	if err := Validate("order-mapping-channels", unknown); err == nil {
		t.Fatal("unoperated warehouse address codes must not be quoted")
	}
	place := map[string]any{
		"expressChannelCode": "CHANNEL-1", "preRequestId": "PRE-1",
		"packageInfoList": []any{map[string]any{"orderNo": "ORDER-1", "goodsIds": []any{1}}},
	}
	if err := Validate("place-express-order", place); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentCheckAndPrintFields(t *testing.T) {
	if err := Validate("check-express-order", map[string]any{"placeRequestId": "PLACE-1"}); err != nil {
		t.Fatal(err)
	}
	if err := Validate("check-express-order", map[string]any{"deliveryNo": "DELIVERY-1"}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []map[string]any{
		{"packageNo": "PKG-1"},
		{"waybillNo": "WAYBILL-1"},
		{"trackingNo": "TRACKING-1"},
	} {
		if err := Validate("check-express-order", invalid); err == nil {
			t.Fatalf("unsupported check fields must fail: %#v", invalid)
		}
	}
	if err := Validate("print-express-info", map[string]any{"deliveryNo": "DELIVERY-1"}); err != nil {
		t.Fatal(err)
	}
	if err := Validate("print-express-info", map[string]any{"orderNo": "ORDER-1", "packageNo": []any{"PKG-1"}}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []map[string]any{
		{"orderNo": "ORDER-1"},
		{"orderNo": "ORDER-1", "packageNo": "PKG-1"},
	} {
		if err := Validate("print-express-info", invalid); err == nil {
			t.Fatalf("invalid print fields must fail: %#v", invalid)
		}
	}
}

func clone(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
