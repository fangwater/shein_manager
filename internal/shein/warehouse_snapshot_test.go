package shein

import (
	"context"
	"testing"
)

func TestDynamicWarehouseSnapshotIsImmutableAndShopScoped(t *testing.T) {
	mapA := map[string]WarehouseMappingSnapshot{"WH-NEW-HOUSTON": {OMSCode: "ARP06A", Revision: 2, Enabled: true}}
	a := WithWarehouseSnapshot(context.Background(), mapA)
	b := WithWarehouseSnapshot(context.Background(), map[string]WarehouseMappingSnapshot{})
	mapA["WH-NEW-HOUSTON"] = WarehouseMappingSnapshot{OMSCode: "ARPGA"}
	if PolicyWarehouseKeyInContext(a, "WH-NEW-HOUSTON", "") != "ARP_HOUSTON" || PolicyWarehouseKeyInContext(b, "WH-NEW-HOUSTON", "") != "" {
		t.Fatal("mapping leaked across shops or mutated")
	}
	if ResolvedOMSWarehouseCode("WH-NEW-HOUSTON", "") != "" {
		t.Fatal("global mapping mutated")
	}
}
func TestDynamicWarehouseValidationAndAvailabilityRespectPause(t *testing.T) {
	ctx := WithWarehouseSnapshot(context.Background(), map[string]WarehouseMappingSnapshot{"WH-NEW-HOUSTON": {OMSCode: "ARP06A", Enabled: true}})
	data := map[string]any{"orderNo": "test", "warehouseAddressCode": "WH-NEW-HOUSTON", "packageSizeInfo": map[string]any{"packageHeight": "1", "packageLength": "1", "packageWidth": "1", "unit": "cm"}, "packageWeightInfo": map[string]any{"packageWeight": "100", "unit": "g"}}
	if err := ValidateWithWarehouseSnapshot(ctx, "order-mapping-channels", data); err != nil {
		t.Fatal(err)
	}
	empty := WithWarehouseSnapshot(ctx, map[string]WarehouseMappingSnapshot{})
	if err := ValidateWithWarehouseSnapshot(empty, "order-mapping-channels", data); err == nil {
		t.Fatal("paused shop quoted")
	}
	result := map[string]any{"info": []any{map[string]any{"warehouseAddressCode": "WH-NEW-HOUSTON", "availableStatus": "1"}, map[string]any{"warehouseAddressCode": "WH-OTHER-SHOP", "availableStatus": "1"}}}
	RestrictShippingWarehouseAvailabilityWithSnapshot(ctx, result)
	rows := warehouseObjects(result["info"])
	if rows[0]["availableStatus"] != "1" || rows[0]["omsWarehouseCode"] != "ARP06A" || rows[1]["availableStatus"] != "0" {
		t.Fatal("dynamic availability leaked")
	}
}
func TestBoughtLabelUsesOriginalSnapshot(t *testing.T) {
	record := LabelPurchaseRecord{SelectedWarehouseAddressCode: "WH-OLD", OMSWarehouseCode: "ARP06A"}
	got := record.ResolvedWarehouse()
	if !got.OK() || got.OMSCode != "ARP06A" || got.AddressCode != "WH-OLD" {
		t.Fatal("history follows mutable mapping")
	}
}
func TestDynamicPhysicalCarrierCapUsesPolicySnapshot(t *testing.T) {
	g := WarehouseCarrierPolicies{WarehouseKey: "ARP_HOUSTON", BaseRules: WarehouseCarrierRules{WarehouseKey: "ARP_HOUSTON", AllowedCarrierCodes: []string{"SPEEDX", "USPS"}}}
	if ChannelUnavailableReason("SPEEDX", "", "", "USD", "WH-NEW", "", false, g) == "" {
		t.Fatal("opaque warehouse bypassed physical cap")
	}
}
