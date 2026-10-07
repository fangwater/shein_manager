package shein

import (
	"context"
	"errors"
	"strings"
)

type WarehouseMappingSnapshot struct {
	OMSCode  string
	Revision int64
	Enabled  bool
}
type warehouseSnapshotKey struct{}

func WithWarehouseSnapshot(ctx context.Context, mappings map[string]WarehouseMappingSnapshot) context.Context {
	snapshot := make(map[string]WarehouseMappingSnapshot, len(mappings))
	for k, v := range mappings {
		snapshot[normalizedWarehouseCode(k)] = v
	}
	return context.WithValue(ctx, warehouseSnapshotKey{}, snapshot)
}
func WarehouseSnapshot(ctx context.Context, code string) (WarehouseMappingSnapshot, bool) {
	m, scoped := ctx.Value(warehouseSnapshotKey{}).(map[string]WarehouseMappingSnapshot)
	if !scoped {
		return WarehouseMappingSnapshot{OMSCode: ResolvedOMSWarehouseCode(code, ""), Enabled: true}, false
	}
	v, ok := m[normalizedWarehouseCode(code)]
	return v, ok
}
func ResolvedOMSWarehouseCodeInContext(ctx context.Context, code, name string) string {
	if _, scoped := ctx.Value(warehouseSnapshotKey{}).(map[string]WarehouseMappingSnapshot); scoped {
		b, ok := WarehouseSnapshot(ctx, code)
		if ok {
			return b.OMSCode
		}
		return ""
	}
	return ResolvedOMSWarehouseCode(code, name)
}
func PolicyWarehouseKeyInContext(ctx context.Context, code, name string) string {
	return PolicyWarehouseKey(ResolvedOMSWarehouseCodeInContext(ctx, code, name), "")
}
func ValidateWithWarehouseSnapshot(ctx context.Context, operation string, data map[string]any) error {
	if operation != "order-mapping-channels" {
		return Validate(operation, data)
	}
	if _, scoped := ctx.Value(warehouseSnapshotKey{}).(map[string]WarehouseMappingSnapshot); !scoped {
		return Validate(operation, data)
	}
	b, ok := WarehouseSnapshot(ctx, warehouseField(data, "warehouseAddressCode"))
	if !ok || !b.Enabled {
		return errors.New("当前店铺仓库未配置或已暂停")
	}
	local := make(map[string]any, len(data)+1)
	for k, v := range data {
		local[k] = v
	}
	local["warehouseName"] = b.OMSCode
	return Validate(operation, local)
}
func RestrictShippingWarehouseAvailabilityWithSnapshot(ctx context.Context, result map[string]any) {
	if _, scoped := ctx.Value(warehouseSnapshotKey{}).(map[string]WarehouseMappingSnapshot); !scoped {
		RestrictShippingWarehouseAvailability(result)
		return
	}
	for _, w := range warehouseObjects(result["info"]) {
		b, ok := WarehouseSnapshot(ctx, warehouseField(w, "warehouseAddressCode", "warehouseCode"))
		if ok {
			w["omsWarehouseCode"] = b.OMSCode
		}
		if !ok || !b.Enabled || IsPGWarehouse(warehouseField(w, "warehouseAddressCode"), warehouseField(w, "warehouseName")) {
			w["availableStatus"] = "0"
			w["unavailableReason"] = "当前店铺未启用该发货仓"
			w["reason"] = "当前店铺未启用该发货仓"
		}
	}
}

// Historical registrations are imported only for the verified existing shop;
// other shops require their own platform discovery and explicit configuration.
func LegacyWarehouseMappingsForShop(shop string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(shop) != "beauty-hangers-home" {
		return out
	}
	for address, code := range omsWarehouseAddressCodes {
		if strings.HasPrefix(address, "WH") {
			out[address] = code
		}
	}
	return out
}
