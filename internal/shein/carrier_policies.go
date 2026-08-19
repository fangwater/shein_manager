package shein

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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
	SupportedPolicyWarehouseKeys = []string{"DPS002", "ARP_EAST", "DPS004", "ARP_WEST"}

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

func DefaultCarrierEnabled(warehouseKey, carrierCode string) bool {
	warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
	carrierCode = strings.ToUpper(strings.TrimSpace(carrierCode))
	if warehouseKey == "ARP_EAST" && (carrierCode == "SWIFTX" || carrierCode == "UNIUNI") {
		return false
	}
	return carrierCode != "UNIUNI"
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

func DefaultCarrierPolicies(warehouseKey string) []CarrierPolicy {
	warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
	policies := make([]CarrierPolicy, 0, len(SupportedAutomaticCarrierCodes))
	for index, code := range SupportedAutomaticCarrierCodes {
		policies = append(policies, CarrierPolicy{
			WarehouseKey: warehouseKey,
			CarrierCode:  code,
			Priority:     index + 1,
			Enabled:      DefaultCarrierEnabled(warehouseKey, code),
		})
	}
	return policies
}

func MergeCarrierPolicies(stored []CarrierPolicy) []WarehouseCarrierPolicies {
	byWarehouse := make(map[string][]CarrierPolicy)
	for _, policy := range stored {
		key := strings.ToUpper(strings.TrimSpace(policy.WarehouseKey))
		byWarehouse[key] = append(byWarehouse[key], policy)
	}
	groups := make([]WarehouseCarrierPolicies, 0, len(SupportedPolicyWarehouseKeys))
	for _, warehouseKey := range SupportedPolicyWarehouseKeys {
		policies, err := ValidateCarrierPolicies(warehouseKey, byWarehouse[warehouseKey])
		if err != nil {
			policies = DefaultCarrierPolicies(warehouseKey)
		}
		groups = append(groups, WarehouseCarrierPolicies{WarehouseKey: warehouseKey, Carriers: policies})
	}
	return groups
}

func ValidateCarrierPolicies(warehouseKey string, policies []CarrierPolicy) ([]CarrierPolicy, error) {
	warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
	if !containsText(SupportedPolicyWarehouseKeys, warehouseKey) {
		return nil, errors.New("未知 OMS 仓库")
	}
	if len(policies) != len(SupportedAutomaticCarrierCodes) {
		return nil, fmt.Errorf("必须配置全部 %d 个快递", len(SupportedAutomaticCarrierCodes))
	}
	seenCodes := make(map[string]bool, len(policies))
	seenPriorities := make(map[int]bool, len(policies))
	normalized := make([]CarrierPolicy, 0, len(policies))
	for _, policy := range policies {
		code := strings.ToUpper(strings.TrimSpace(policy.CarrierCode))
		if !automaticCarrierWhitelist[code] {
			return nil, fmt.Errorf("不支持的自动发货快递 %q", policy.CarrierCode)
		}
		if seenCodes[code] {
			return nil, fmt.Errorf("快递 %s 重复", code)
		}
		if policy.Priority < 1 || policy.Priority > len(SupportedAutomaticCarrierCodes) || seenPriorities[policy.Priority] {
			return nil, errors.New("快递优先级必须是不重复的连续序号")
		}
		seenCodes[code] = true
		seenPriorities[policy.Priority] = true
		normalized = append(normalized, CarrierPolicy{
			WarehouseKey: warehouseKey,
			CarrierCode:  code,
			Priority:     policy.Priority,
			Enabled:      policy.Enabled,
		})
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Priority < normalized[j].Priority })
	return normalized, nil
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
			return fmt.Sprintf("%s 店铺策略已在 %s 仓库禁用", code, warehouseKey)
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

func (s *Store) ListCarrierPolicies(ctx context.Context, shopKey string) ([]CarrierPolicy, error) {
	shopKey = strings.TrimSpace(shopKey)
	if shopKey == "" {
		return nil, errors.New("shop is required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT oms_warehouse_key, carrier_code, priority, enabled
		FROM shein_go_carrier_policies
		WHERE shop_key = $1
		ORDER BY oms_warehouse_key, priority, carrier_code
	`, shopKey)
	if err != nil {
		return nil, fmt.Errorf("list SHEIN carrier policies: %w", err)
	}
	defer rows.Close()
	items := make([]CarrierPolicy, 0)
	for rows.Next() {
		var item CarrierPolicy
		if err := rows.Scan(&item.WarehouseKey, &item.CarrierCode, &item.Priority, &item.Enabled); err != nil {
			return nil, fmt.Errorf("scan SHEIN carrier policy: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ReplaceCarrierPolicies(ctx context.Context, shopKey, warehouseKey string, policies []CarrierPolicy) error {
	shopKey = strings.TrimSpace(shopKey)
	warehouseKey = strings.ToUpper(strings.TrimSpace(warehouseKey))
	if shopKey == "" {
		return errors.New("shop is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin SHEIN carrier policies: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		DELETE FROM shein_go_carrier_policies
		WHERE shop_key = $1 AND oms_warehouse_key = $2
	`, shopKey, warehouseKey); err != nil {
		return fmt.Errorf("clear SHEIN carrier policies: %w", err)
	}
	for _, policy := range policies {
		if _, err := tx.Exec(ctx, `
			INSERT INTO shein_go_carrier_policies (
				shop_key, oms_warehouse_key, carrier_code, priority, enabled, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6)
		`, shopKey, warehouseKey, policy.CarrierCode, policy.Priority, policy.Enabled, time.Now()); err != nil {
			return fmt.Errorf("store SHEIN carrier policy: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit SHEIN carrier policies: %w", err)
	}
	return nil
}

func (s *Store) ShippingQuoteWarehouse(ctx context.Context, shopKey, preRequestID string) (string, error) {
	shopKey = strings.TrimSpace(shopKey)
	preRequestID = strings.TrimSpace(preRequestID)
	if shopKey == "" || preRequestID == "" {
		return "", errors.New("shop and preRequestId are required")
	}
	var warehouse string
	err := s.pool.QueryRow(ctx, `
		SELECT warehouse_address_code
		FROM shein_go_shipping_quotes
		WHERE shop_key = $1 AND pre_request_id = $2
	`, shopKey, preRequestID).Scan(&warehouse)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("load SHEIN quote warehouse: %w", err)
	}
	return warehouse, nil
}

func (s *Store) ListMergedCarrierPolicies(ctx context.Context, shopKey string) ([]WarehouseCarrierPolicies, error) {
	stored, err := s.ListCarrierPolicies(ctx, shopKey)
	if err != nil {
		return nil, err
	}
	return MergeCarrierPolicies(stored), nil
}

func (s *Store) UpdateCarrierPolicies(ctx context.Context, shopKey, warehouseKey string, policies []CarrierPolicy) (WarehouseCarrierPolicies, error) {
	normalized, err := ValidateCarrierPolicies(warehouseKey, policies)
	if err != nil {
		return WarehouseCarrierPolicies{}, err
	}
	if err := s.ReplaceCarrierPolicies(ctx, shopKey, normalized[0].WarehouseKey, normalized); err != nil {
		return WarehouseCarrierPolicies{}, err
	}
	return WarehouseCarrierPolicies{WarehouseKey: normalized[0].WarehouseKey, Carriers: normalized}, nil
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
