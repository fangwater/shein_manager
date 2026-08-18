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

var dpsWarehouseAddressCodes = map[string]string{
	"WH2604283535967233": "DPSNY002",
	"WH2603303477748739": "DPSCA004",
	"DPSNY002":           "DPSNY002",
	"DPSCA004":           "DPSCA004",
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

func RequiresManualParcelCreate(shopKey, code, name string) bool {
	if !shopRequiresManualParcelCreate(shopKey) {
		return false
	}
	return ResolvedDPSWarehouseCode(code, name) != ""
}

func ResolvedDPSWarehouseCode(code, name string) string {
	if mapped, ok := dpsWarehouseAddressCodes[normalizedWarehouseCode(code)]; ok {
		return mapped
	}
	identity := warehouseIdentity(code, name)
	if identity == "" || !strings.Contains(identity, "DPS") {
		return ""
	}
	if strings.Contains(identity, "DPSNY002") || strings.Contains(identity, "DPS002") {
		return "DPSNY002"
	}
	if strings.Contains(identity, "DPSCA004") || strings.Contains(identity, "DPS004") {
		return "DPSCA004"
	}
	return ""
}

func shopRequiresManualParcelCreate(shopKey string) bool {
	switch strings.TrimSpace(shopKey) {
	case "", "default", "beauty-hangers-home":
		return true
	default:
		return false
	}
}

func OMSAccountForWarehouse(code, name string) string {
	if ResolvedDPSWarehouseCode(code, name) != "" {
		return "dps"
	}
	if IsOperatedShippingWarehouse(code, name) && !IsPGWarehouse(code, name) {
		return "arp"
	}
	return ""
}

func OppositeOMSAccount(account string) string {
	switch strings.ToLower(strings.TrimSpace(account)) {
	case "dps":
		return "arp"
	case "arp":
		return "dps"
	default:
		return ""
	}
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
