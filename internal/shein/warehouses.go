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

func IsPGWarehouse(code, name string) bool {
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
