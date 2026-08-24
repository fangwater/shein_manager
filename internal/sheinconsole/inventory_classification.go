package sheinconsole

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"shein-api-manager/internal/shein"
	"shein-api-manager/internal/xlwms"
)

const inventoryClassificationInterval = 5 * time.Minute

type inventoryClassificationStats struct {
	Checked  int
	Eligible int
	Manual   int
	Failed   int
}

type inventoryCheckGroup struct {
	quantities map[string]int
	orders     []shein.OrderQueueItem
}

type inventoryManualRequiredError struct {
	reason string
}

func (err *inventoryManualRequiredError) Error() string {
	return err.reason
}

func (s *Server) startInventoryClassification() {
	if s.xlwms == nil {
		return
	}
	go func() {
		run := func() {
			ctx, cancel := context.WithTimeout(context.Background(), s.requestTimeout*4)
			defer cancel()
			stats, err := s.classifyFulfillmentInventory(ctx, s.shopKey)
			if err != nil {
				s.logger.Warn("classify SHEIN fulfillment inventory failed", "shop", s.shopKey, "error", sanitizedError(err))
				return
			}
			if stats.Checked > 0 {
				s.logger.Info("classified SHEIN fulfillment inventory", "shop", s.shopKey,
					"checked", stats.Checked, "eligible", stats.Eligible, "manual", stats.Manual, "failed", stats.Failed)
			}
		}
		run()
		ticker := time.NewTicker(inventoryClassificationInterval)
		defer ticker.Stop()
		for range ticker.C {
			run()
		}
	}()
}

func (s *Server) classifyFulfillmentInventory(ctx context.Context, shopKey string) (inventoryClassificationStats, error) {
	if s.xlwms == nil {
		return inventoryClassificationStats{}, errors.New("领星实时库存查询服务未配置")
	}
	orders, err := s.store.ListOrderQueue(ctx, shopKey, "all")
	if err != nil {
		return inventoryClassificationStats{}, err
	}
	stats := inventoryClassificationStats{}
	groups := make(map[string]*inventoryCheckGroup)
	for _, order := range orders {
		// A bought or active label must continue on its selected warehouse unchanged.
		if !order.EligibleBeforeInventoryCheck() || (order.Job != nil && order.Job.Status != "failed" && order.Job.Status != "manual_required") {
			continue
		}
		quantities, missing := warehouseQuantities(order)
		if len(missing) > 0 || len(quantities) == 0 {
			check := shein.InventoryCheck{
				SourceDetailFetchedAt: order.DetailFetchedAt,
				Status:                "manual",
				Categories:            []string{"sku_unbound"},
				ReasonDetails:         []string{"订单商品缺少仓库 SKU 映射"},
			}
			if err := s.store.SaveInventoryCheck(ctx, shopKey, order.OrderNo, check); err != nil {
				return stats, err
			}
			stats.Checked++
			stats.Manual++
			continue
		}
		key := inventoryQuantityKey(quantities)
		group := groups[key]
		if group == nil {
			group = &inventoryCheckGroup{quantities: quantities}
			groups[key] = group
		}
		group.orders = append(group.orders, order)
	}
	for _, group := range groups {
		items := inventoryItems(group.quantities)
		decision, queryErr := s.xlwms.QueryInventoryForShop(ctx, "shein", shopKey, items)
		check := automaticInventoryCheck(decision, group.quantities, queryErr)
		for _, order := range group.orders {
			check.SourceDetailFetchedAt = order.DetailFetchedAt
			if err := s.store.SaveInventoryCheck(ctx, shopKey, order.OrderNo, check); err != nil {
				return stats, err
			}
			stats.Checked++
			switch check.Status {
			case "eligible":
				stats.Eligible++
			case "manual":
				stats.Manual++
			default:
				stats.Failed++
			}
		}
	}
	return stats, nil
}

func inventoryQuantityKey(quantities map[string]int) string {
	keys := make([]string, 0, len(quantities))
	for sku := range quantities {
		keys = append(keys, sku)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, sku := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", sku, quantities[sku]))
	}
	return strings.Join(parts, "\x00")
}

func inventoryItems(quantities map[string]int) []xlwms.InventoryItem {
	items := make([]xlwms.InventoryItem, 0, len(quantities))
	for sku, quantity := range quantities {
		items = append(items, xlwms.InventoryItem{WarehouseSKU: sku, Quantity: quantity})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].WarehouseSKU < items[j].WarehouseSKU })
	return items
}

func automaticInventoryCheck(raw json.RawMessage, quantities map[string]int, queryErr error) shein.InventoryCheck {
	if queryErr != nil {
		return shein.InventoryCheck{Status: "failed", ErrorMessage: "领星实时库存查询失败"}
	}
	var decision automaticInventoryDecision
	if err := json.Unmarshal(raw, &decision); err != nil {
		return shein.InventoryCheck{Status: "failed", ErrorMessage: "领星库存响应无法解析"}
	}
	if !decision.Complete {
		return shein.InventoryCheck{Status: "failed", ErrorMessage: "领星实时库存查询不完整"}
	}
	if !decision.PackageResolution.Complete {
		reason := strings.TrimSpace(decision.PackageResolution.Error)
		if reason == "" {
			reason = "仓库 SKU 包裹规格不完整"
		}
		return shein.InventoryCheck{
			Status: "manual", Categories: []string{"warehouse_sku_spec_incomplete"}, ReasonDetails: []string{reason},
		}
	}
	reasons := make([]string, 0)
	for _, record := range decision.Records {
		if !record.RequiresManual {
			continue
		}
		reason := strings.TrimSpace(record.Reason)
		if reason == "" {
			reason = fmt.Sprintf("SKU %s 库存未通过自动发货安全线", record.SKU)
		}
		reasons = append(reasons, reason)
	}
	if len(reasons) > 0 {
		return shein.InventoryCheck{Status: "manual", Categories: []string{"inventory_rule"}, ReasonDetails: reasons}
	}
	if _, err := automaticInventoryWarehouseKeys(raw, quantities); err != nil {
		return shein.InventoryCheck{
			Status: "manual", Categories: []string{"inventory_rule"}, ReasonDetails: []string{err.Error()},
		}
	}
	return shein.InventoryCheck{Status: "eligible", Categories: []string{}, ReasonDetails: []string{}}
}

func inventoryManualError(check shein.InventoryCheck) error {
	reason := "库存规则要求转人工处理"
	if len(check.ReasonDetails) > 0 && strings.TrimSpace(check.ReasonDetails[0]) != "" {
		reason = check.ReasonDetails[0]
	}
	return &inventoryManualRequiredError{reason: reason}
}
