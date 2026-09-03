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

type WarehouseCarrierPolicies struct {
	WarehouseKey string          `json:"warehouse_key"`
	Carriers     []CarrierPolicy `json:"carriers"`
}

var (
	SupportedAutomaticCarrierCodes = []string{"GOFO", "SWIFTX", "SPEEDX", "YANWEN", "UPS", "USPS", "FEDEX"}

	automaticCarrierWhitelist = map[string]bool{
		"GOFO": true, "SPEEDX": true, "SWIFTX": true, "YANWEN": true,
		"UPS": true, "USPS": true, "FEDEX": true,
	}

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
	return len(SupportedAutomaticCarrierCodes) + 1
}

func ChannelUnavailableReason(channelCode, expressIDCode, expressShortName, warehouseCode, warehouseName string, policies []CarrierPolicy) string {
	code := CarrierCode(channelCode, expressIDCode, expressShortName)
	warehouseKey := PolicyWarehouseKey(warehouseCode, warehouseName)
	switch {
	case code == "UNIUNI":
		return "UNIUNI 已禁用"
	case !automaticCarrierWhitelist[code]:
		return "不在自动发货物流白名单"
	}
	for _, policy := range policies {
		if policy.CarrierCode == code && !policy.Enabled {
			if warehouseKey == "" {
				warehouseKey = policy.WarehouseKey
			}
			return fmt.Sprintf("SHEIN 发货策略已在 %s 仓库禁用 %s", warehouseKey, code)
		}
	}
	return ""
}

func ApplyCarrierPoliciesToChannels(result map[string]any, warehouseCode, warehouseName string, policies []CarrierPolicy) {
	if result == nil {
		return
	}
	for _, channel := range channelObjects(result["info"]) {
		reason := ChannelUnavailableReason(
			warehouseField(channel, "expressChannelCode", "channelCode", "carrierCode"),
			warehouseField(channel, "expressIdCode", "expressName"),
			warehouseField(channel, "expressShortName"),
			warehouseCode, warehouseName, policies,
		)
		if reason == "" {
			continue
		}
		channel["unavailableReason"] = reason
		channel["availableStatus"] = "0"
	}
}

func PoliciesByWarehouse(groups []WarehouseCarrierPolicies) map[string][]CarrierPolicy {
	result := make(map[string][]CarrierPolicy, len(groups))
	for _, group := range groups {
		result[group.WarehouseKey] = group.Carriers
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
