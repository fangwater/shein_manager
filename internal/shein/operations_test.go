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
		"orderNo": "ORDER-1", "warehouseAddressCode": "WH-1",
		"packageSizeInfo":   map[string]any{"packageLength": "10", "packageWidth": "8", "packageHeight": "2", "unit": "cm"},
		"packageWeightInfo": map[string]any{"packageWeight": "200.5", "unit": "g"},
	}
	if err := Validate("order-mapping-channels", channels); err != nil {
		t.Fatal(err)
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
	if err := Validate("check-express-order", map[string]any{"packageNo": "PKG-1"}); err != nil {
		t.Fatal(err)
	}
	if err := Validate("print-express-info", map[string]any{"deliveryNo": "DELIVERY-1"}); err != nil {
		t.Fatal(err)
	}
	if err := Validate("print-express-info", map[string]any{"orderNo": "ORDER-1", "packageNo": "PKG-1"}); err != nil {
		t.Fatal(err)
	}
	if err := Validate("print-express-info", map[string]any{"orderNo": "ORDER-1"}); err == nil {
		t.Fatal("orderNo without packageNo must fail")
	}
}

func clone(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
