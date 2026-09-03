package shein

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type CarrierPolicy struct {
	WarehouseKey string `json:"warehouse_key"`
	CarrierCode  string `json:"carrier_code"`
	Priority     int    `json:"priority"`
	Enabled      bool   `json:"enabled"`
}

type WarehouseCarrierRules struct {
	WarehouseKey         string   `json:"warehouse_key"`
	AllowedCarrierCodes  []string `json:"allowed_carrier_codes"`
	AllowSignature       bool     `json:"allow_signature"`
	AllowedCurrencyCodes []string `json:"allowed_currency_codes"`
	SelectionMode        string   `json:"selection_mode"`
	MaxPriceDelta        float64  `json:"max_price_delta"`
	WarehouseTiePriority int      `json:"warehouse_tie_priority"`
}

type WarehouseCarrierPolicies struct {
	WarehouseKey string                `json:"warehouse_key"`
	BaseRules    WarehouseCarrierRules `json:"base_rules"`
	Carriers     []CarrierPolicy       `json:"carriers"`
}

var (
	omsToPolicyWarehouseKey = map[string]string{
		"DPSNY002": "DPS002",
		"HYTX30":   "ARP_EAST",
		"DPSCA004": "DPS004",
		"ARPCA01":  "ARP_WEST",
	}

	carrierMatchOrder = []string{"UNIUNI", "SWIFTX", "SPEEDX", "YANWEN", "FEDEX", "USPS", "UPS", "GOFO"}
)

func PolicyWarehouseKey(code, name string) string {
	if key, ok := omsToPolicyWarehouseKey[ResolvedOMSWarehouseCode(code, name)]; ok {
		return key
	}
	return ""
}

func IsARPPolicyWarehouse(warehouseKey string) bool {
	switch strings.ToUpper(strings.TrimSpace(warehouseKey)) {
	case "ARP_EAST", "ARP_WEST", "HYTX30", "ARPCA01":
		return true
	default:
		return false
	}
}

func CarrierCode(values ...string) string {
	text := strings.ToUpper(strings.Join(values, " "))
	normalized := strings.NewReplacer(" ", "", "-", "", "_", "", "/", "").Replace(text)
	for _, code := range carrierMatchOrder {
		if strings.Contains(normalized, code) {
			return code
		}
	}
	return ""
}

func CarrierCodeFromChannel(channel map[string]any) string {
	return CarrierCode(
		warehouseField(channel, "expressChannelCode", "channelCode", "carrierCode"),
		warehouseField(channel, "expressIdCode", "expressName"),
		warehouseField(channel, "expressShortName"),
	)
}

func ConfiguredCarrierPriority(policies []CarrierPolicy, code string) int {
	for _, policy := range policies {
		if policy.CarrierCode == code {
			return policy.Priority
		}
	}
	return len(policies) + 1
}

func ChannelUnavailableReason(channelCode, expressIDCode, expressShortName, currencyCode, warehouseCode, warehouseName string, signatureRequired bool, group WarehouseCarrierPolicies) string {
	code := CarrierCode(channelCode, expressIDCode, expressShortName)
	displayCode := code
	if displayCode == "" {
		displayCode = strings.ToUpper(strings.TrimSpace(channelCode))
	}
	warehouseKey := PolicyWarehouseKey(warehouseCode, warehouseName)
	allowedCarriers := make(map[string]bool, len(group.BaseRules.AllowedCarrierCodes))
	for _, value := range group.BaseRules.AllowedCarrierCodes {
		allowedCarriers[strings.ToUpper(strings.TrimSpace(value))] = true
	}
	if !allowedCarriers[code] {
		return fmt.Sprintf("XLWMS %s 基础规则未允许承运商 %s", group.BaseRules.WarehouseKey, displayCode)
	}
	if signatureRequired && !group.BaseRules.AllowSignature {
		return fmt.Sprintf("XLWMS %s 基础规则禁止签名服务", group.BaseRules.WarehouseKey)
	}
	if currencyCode = strings.ToUpper(strings.TrimSpace(currencyCode)); currencyCode != "" && len(group.BaseRules.AllowedCurrencyCodes) > 0 {
		allowed := false
		for _, value := range group.BaseRules.AllowedCurrencyCodes {
			allowed = allowed || strings.EqualFold(strings.TrimSpace(value), currencyCode)
		}
		if !allowed {
			return fmt.Sprintf("XLWMS %s 基础规则不允许报价币种 %s", group.BaseRules.WarehouseKey, currencyCode)
		}
	}
	for _, policy := range group.Carriers {
		if policy.CarrierCode == code && !policy.Enabled {
			if warehouseKey == "" {
				warehouseKey = policy.WarehouseKey
			}
			return fmt.Sprintf("SHEIN 发货策略已在 %s 仓库禁用 %s", warehouseKey, code)
		}
	}
	return ""
}

func ApplyCarrierPoliciesToChannels(result map[string]any, warehouseCode, warehouseName string, group WarehouseCarrierPolicies) {
	if result == nil {
		return
	}
	for _, channel := range channelObjects(result["info"]) {
		reason := ChannelUnavailableReason(
			warehouseField(channel, "expressChannelCode", "channelCode", "carrierCode"),
			warehouseField(channel, "expressIdCode", "expressName"),
			warehouseField(channel, "expressShortName"),
			warehouseField(channel, "currencyCode"), warehouseCode, warehouseName,
			channelRequiresSignature(channel), group,
		)
		if reason == "" {
			continue
		}
		channel["unavailableReason"] = reason
		channel["availableStatus"] = "0"
	}
}

func channelRequiresSignature(channel map[string]any) bool {
	for _, key := range []string{"signatureOnDelivery", "needSignature", "signatureRequired"} {
		switch value := channel[key].(type) {
		case bool:
			if value {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(value), "true") || strings.TrimSpace(value) == "1" {
				return true
			}
		case float64:
			if value != 0 {
				return true
			}
		}
	}
	if value, exists := channel["signServiceId"]; exists {
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" && text != "0" && text != "<nil>" {
			return true
		}
	}
	if value := warehouseField(channel, "signServiceId"); value != "" && value != "0" {
		return true
	}
	if fields, ok := channel["infoNeeded"].([]any); ok {
		for _, field := range fields {
			if strings.EqualFold(strings.TrimSpace(fmt.Sprint(field)), "signServiceId") {
				return true
			}
		}
	}
	if fields, ok := channel["infoNeeded"].([]string); ok {
		for _, field := range fields {
			if strings.EqualFold(strings.TrimSpace(field), "signServiceId") {
				return true
			}
		}
	}
	return false
}

func PoliciesByWarehouse(groups []WarehouseCarrierPolicies) map[string]WarehouseCarrierPolicies {
	result := make(map[string]WarehouseCarrierPolicies, len(groups))
	for _, group := range groups {
		result[group.WarehouseKey] = group
	}
	return result
}

func (s *Store) ShippingQuoteWarehouseAndOrder(ctx context.Context, shopKey, preRequestID string) (string, string, error) {
	shopKey = strings.TrimSpace(shopKey)
	preRequestID = strings.TrimSpace(preRequestID)
	if shopKey == "" || preRequestID == "" {
		return "", "", errors.New("shop and preRequestId are required")
	}
	var warehouse, orderNo string
	err := s.pool.QueryRow(ctx, `
		SELECT warehouse_address_code, order_no
		FROM shein_go_shipping_quotes
		WHERE shop_key = $1 AND pre_request_id = $2
	`, shopKey, preRequestID).Scan(&warehouse, &orderNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	if err != nil {
		return "", "", fmt.Errorf("load SHEIN quote warehouse: %w", err)
	}
	return warehouse, orderNo, nil
}

func channelObjects(value any) []map[string]any {
	objects := make([]map[string]any, 0)
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			if warehouseField(typed, "expressChannelCode", "channelCode") != "" {
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

func containsText(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
