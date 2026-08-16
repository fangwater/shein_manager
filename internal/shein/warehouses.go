package shein

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	pgWarehouseUnavailableReason         = "PG仓不在实际发货范围内，不能选择"
	unoperatedWarehouseUnavailableReason = "非实际发货仓，不能用于平台面单"
)

var pgWarehousePattern = regexp.MustCompile(`(?:^|[^A-Z0-9])PG\d+`)

// SHEIN quotes by opaque warehouseAddressCode values. DPS/ARP tokens usually
// appear only in warehouseName, so channel queries must also recognize the
// operated address IDs themselves.
var operatedWarehouseAddressCodes = map[string]struct{}{
	"WH2604283535967233": {},
	"WH2603303477748739": {},
	"WH2607084039788546": {},
	"WH2608123417047040": {},
	"DPSNY002":           {},
	"DPSCA004":           {},
	"HYTX30":             {},
	"ARPCA01":            {},
}

var pgWarehouseAddressCodes = map[string]struct{}{
	"WH2602103441974274": {},
	"PG1955":             {},
}

func warehouseIdentity(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.ToUpper(strings.Join(parts, " "))
}

func warehouseText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func warehouseField(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := warehouseText(object[key]); text != "" {
			return text
		}
	}
	return ""
}

func normalizedWarehouseCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func IsPGWarehouse(code, name string) bool {
	if _, ok := pgWarehouseAddressCodes[normalizedWarehouseCode(code)]; ok {
		return true
	}
	identity := warehouseIdentity(code, name)
	if identity == "" {
		return false
	}
	if strings.Contains(identity, "PG仓") || strings.Contains(identity, "PG 仓") {
		return true
	}
	return pgWarehousePattern.MatchString(identity)
}

func IsOperatedShippingWarehouse(code, name string) bool {
	if _, ok := operatedWarehouseAddressCodes[normalizedWarehouseCode(code)]; ok {
		return true
	}
	identity := warehouseIdentity(code, name)
	if identity == "" {
		return false
	}
	return strings.Contains(identity, "DPS") || strings.Contains(identity, "ARP")
}

func IsAllowedShippingWarehouse(code, name string) bool {
	return IsOperatedShippingWarehouse(code, name) && !IsPGWarehouse(code, name)
}

func RestrictShippingWarehouseAvailability(result map[string]any) {
	if result == nil {
		return
	}
	for _, warehouse := range warehouseObjects(result["info"]) {
		code := warehouseField(warehouse, "warehouseAddressCode", "warehouseCode")
		name := warehouseField(warehouse, "warehouseName", "warehouseAddressName", "warehouseDesc")
		if IsAllowedShippingWarehouse(code, name) {
			continue
		}
		warehouse["availableStatus"] = "0"
		reason := unoperatedWarehouseUnavailableReason
		if IsPGWarehouse(code, name) {
			reason = pgWarehouseUnavailableReason
		}
		warehouse["unavailableReason"] = reason
		warehouse["reason"] = reason
	}
}

func warehouseObjects(value any) []map[string]any {
	objects := make([]map[string]any, 0)
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			if warehouseField(typed, "warehouseAddressCode", "warehouseCode") != "" {
				objects = append(objects, typed)
				return
			}
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(value)
	return objects
}
