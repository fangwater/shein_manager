package shein

import "testing"

func TestIsAllowedShippingWarehouseAcceptsOperatedWarehouses(t *testing.T) {
	cases := [][2]string{
		{"DPSNY002", "DPS达派思-纽约"},
		{"HYTX30", "ARP1号仓-美东PA"},
		{"DPSCA004", "DPS达派思-加州"},
		{"ARPCA01", "ARP8号仓-美西LA"},
	}
	for _, item := range cases {
		if !IsAllowedShippingWarehouse(item[0], item[1]) {
			t.Fatalf("operated warehouse must stay selectable: %#v", item)
		}
	}
}

func TestIsAllowedShippingWarehouseRejectsPGAndUnknownWarehouses(t *testing.T) {
	cases := [][2]string{
		{"PG1955", "PG仓"},
		{"USPG001", "SHEIN PG仓"},
		{"", "PG1955 美西"},
		{"WH-1", "未知仓"},
		{"", ""},
	}
	for _, item := range cases {
		if IsAllowedShippingWarehouse(item[0], item[1]) {
			t.Fatalf("non-operated warehouse must not be selectable: %#v", item)
		}
	}
	if !IsPGWarehouse("PG1955", "平台仓") {
		t.Fatal("PG1955 must be recognized as a PG warehouse")
	}
}

func TestRestrictShippingWarehouseAvailabilityDisablesPGWarehouses(t *testing.T) {
	result := map[string]any{"info": map[string]any{"availableWarehouses": []any{
		map[string]any{"warehouseAddressCode": "DPSNY002", "warehouseName": "DPS达派思-纽约", "availableStatus": "1"},
		map[string]any{"warehouseAddressCode": "PG1955", "warehouseName": "PG仓", "availableStatus": "1"},
		map[string]any{"warehouseAddressCode": "OTHER", "warehouseName": "第三方仓", "availableStatus": "1"},
	}}}
	RestrictShippingWarehouseAvailability(result)
	warehouses := warehouseObjects(result["info"])
	if len(warehouses) != 3 {
		t.Fatalf("warehouse count = %d, want 3", len(warehouses))
	}
	if warehouses[0]["availableStatus"] != "1" {
		t.Fatalf("DPS warehouse was disabled: %#v", warehouses[0])
	}
	if warehouses[1]["availableStatus"] != "0" || warehouses[1]["unavailableReason"] != pgWarehouseUnavailableReason {
		t.Fatalf("PG warehouse was left selectable: %#v", warehouses[1])
	}
	if warehouses[2]["availableStatus"] != "0" || warehouses[2]["unavailableReason"] != unoperatedWarehouseUnavailableReason {
		t.Fatalf("unknown warehouse was left selectable: %#v", warehouses[2])
	}
}
