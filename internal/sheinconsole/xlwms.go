package sheinconsole

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"shein-api-manager/internal/shein"
	"shein-api-manager/internal/xlwms"
)

type xlwmsLookupFailure struct {
	Account string `json:"account"`
	Error   string `json:"error"`
}

type xlwmsLookupResult struct {
	PlatformOrderNo string                      `json:"platform_order_no"`
	Found           bool                        `json:"found"`
	MatchCount      int                         `json:"match_count"`
	Accounts        []xlwms.PlatformOrderLookup `json:"accounts"`
	Failures        []xlwmsLookupFailure        `json:"failures"`
	QueriedAt       time.Time                   `json:"queried_at"`
}

type xlwmsWarehousePreview struct {
	ParentOrderSN    string          `json:"parent_order_sn"`
	Quantities       map[string]int  `json:"quantities"`
	Decision         json.RawMessage `json:"decision"`
	RequiresManual   bool            `json:"requires_manual"`
	ManualReasons    []string        `json:"manual_reasons"`
	ManualCategories []string        `json:"manual_categories"`
	MappingRequired  bool            `json:"mapping_required"`
	Ready            bool            `json:"ready"`
	InventoryError   string          `json:"inventory_error,omitempty"`
	Regions          []any           `json:"regions"`
}

func (s *Server) xlwmsAccounts(writer http.ResponseWriter, request *http.Request) {
	if s.xlwms == nil {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "领星查询服务未配置"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	accounts, err := s.xlwms.Accounts(ctx)
	if err != nil {
		writeXLWMSError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, response{Success: true, Data: accounts})
}

func (s *Server) xlwmsPlatformOrder(writer http.ResponseWriter, request *http.Request) {
	if s.xlwms == nil {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "领星查询服务未配置"})
		return
	}
	orderNo := strings.TrimSpace(request.PathValue("orderNo"))
	if orderNo == "" || len(orderNo) > 100 || strings.ContainsAny(orderNo, "\r\n\t") {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "平台订单号格式无效"})
		return
	}
	account := strings.TrimSpace(request.URL.Query().Get("account"))
	if len(account) > 64 || strings.ContainsAny(account, "\r\n\t") {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "领星账户参数无效"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	accountKeys := []string{account}
	if account == "" || strings.EqualFold(account, "all") {
		accounts, err := s.xlwms.Accounts(ctx)
		if err != nil {
			writeXLWMSError(writer, err)
			return
		}
		accountKeys = accountKeys[:0]
		for _, item := range accounts {
			key := strings.TrimSpace(item.Key)
			if key != "" && len(accountKeys) < 20 {
				accountKeys = append(accountKeys, key)
			}
		}
	}
	if len(accountKeys) == 0 {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "没有可用的领星账户"})
		return
	}

	lookups := make([]xlwms.PlatformOrderLookup, len(accountKeys))
	failures := make([]xlwmsLookupFailure, len(accountKeys))
	succeeded := make([]bool, len(accountKeys))
	var wait sync.WaitGroup
	for index, accountKey := range accountKeys {
		wait.Add(1)
		go func(index int, accountKey string) {
			defer wait.Done()
			lookup, err := s.xlwms.QueryPlatformOrder(ctx, accountKey, orderNo)
			if err != nil {
				failures[index] = xlwmsLookupFailure{Account: accountKey, Error: "查询失败"}
				return
			}
			lookups[index] = lookup
			succeeded[index] = true
		}(index, accountKey)
	}
	wait.Wait()

	result := xlwmsLookupResult{
		PlatformOrderNo: orderNo, Accounts: make([]xlwms.PlatformOrderLookup, 0, len(accountKeys)),
		Failures: make([]xlwmsLookupFailure, 0), QueriedAt: time.Now().UTC(),
	}
	for index := range accountKeys {
		if !succeeded[index] {
			result.Failures = append(result.Failures, failures[index])
			continue
		}
		result.Accounts = append(result.Accounts, lookups[index])
		result.Found = result.Found || lookups[index].Found
		result.MatchCount += lookups[index].MatchCount
	}
	if len(result.Accounts) == 0 {
		writeJSON(writer, http.StatusBadGateway, response{Success: false, Error: "领星订单查询失败"})
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, response{Success: true, Data: result})
}

func (s *Server) xlwmsWarehousePreview(writer http.ResponseWriter, request *http.Request) {
	if s.xlwms == nil {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "领星查询服务未配置"})
		return
	}
	orderNo := strings.TrimSpace(request.PathValue("orderNo"))
	if orderNo == "" {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "平台订单号不能为空"})
		return
	}
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	orders, err := s.store.ListOrderQueue(ctx, shopKey, "all")
	if err != nil {
		s.internalError(writer, "load order for XLWMS warehouse preview", err)
		return
	}
	var order *shein.OrderQueueItem
	for index := range orders {
		if orders[index].OrderNo == orderNo {
			order = &orders[index]
			break
		}
	}
	if order == nil {
		writeJSON(writer, http.StatusNotFound, response{Success: false, Error: "当前店铺中未找到待发货订单"})
		return
	}
	quantities, missing := warehouseQuantities(*order)
	preview := xlwmsWarehousePreview{
		ParentOrderSN: orderNo, Quantities: quantities, Decision: json.RawMessage(`{}`),
		ManualReasons: []string{}, ManualCategories: []string{}, Regions: []any{},
	}
	if len(missing) > 0 {
		preview.RequiresManual = true
		preview.ManualCategories = append(preview.ManualCategories, "sku_unbound")
		preview.ManualReasons = append(preview.ManualReasons, "以下商品尚未绑定仓库 SKU："+strings.Join(missing, "、"))
		writeJSON(writer, http.StatusOK, response{Success: true, Data: preview})
		return
	}
	items := make([]xlwms.InventoryItem, 0, len(quantities))
	for sku, quantity := range quantities {
		items = append(items, xlwms.InventoryItem{WarehouseSKU: sku, Quantity: quantity})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].WarehouseSKU < items[j].WarehouseSKU })
	decision, queryErr := s.xlwms.QueryInventory(ctx, items)
	if queryErr != nil {
		preview.InventoryError = "领星实时库存查询失败"
		writeJSON(writer, http.StatusOK, response{Success: true, Data: preview})
		return
	}
	preview.Decision = decision
	var summary struct {
		Complete bool `json:"complete"`
		Records  []struct {
			SKU            string `json:"sku"`
			RequiresManual bool   `json:"requires_manual"`
			Reason         string `json:"reason"`
		} `json:"records"`
		PackageResolution struct {
			Complete    bool     `json:"complete"`
			Error       string   `json:"error"`
			MissingSKUs []string `json:"missing_skus"`
		} `json:"package_resolution"`
	}
	if err := json.Unmarshal(decision, &summary); err != nil {
		preview.InventoryError = "领星库存响应无法解析"
		writeJSON(writer, http.StatusOK, response{Success: true, Data: preview})
		return
	}
	if !summary.Complete {
		preview.InventoryError = "领星库存查询不完整"
	}
	for _, record := range summary.Records {
		if !record.RequiresManual {
			continue
		}
		preview.RequiresManual = true
		preview.ManualCategories = appendUnique(preview.ManualCategories, "inventory_rule")
		reason := strings.TrimSpace(record.Reason)
		if reason == "" {
			reason = record.SKU + " 库存未通过安全线"
		}
		preview.ManualReasons = append(preview.ManualReasons, reason)
	}
	if !summary.PackageResolution.Complete {
		preview.RequiresManual = true
		preview.ManualCategories = appendUnique(preview.ManualCategories, "warehouse_sku_spec_incomplete")
		reason := strings.TrimSpace(summary.PackageResolution.Error)
		if reason == "" && len(summary.PackageResolution.MissingSKUs) > 0 {
			reason = "仓库 SKU 包裹规格不完整：" + strings.Join(summary.PackageResolution.MissingSKUs, "、")
		}
		if reason != "" {
			preview.ManualReasons = append(preview.ManualReasons, reason)
		}
	}
	preview.Ready = summary.Complete && summary.PackageResolution.Complete && !preview.RequiresManual
	writeJSON(writer, http.StatusOK, response{Success: true, Data: preview})
}

func warehouseQuantities(order shein.OrderQueueItem) (map[string]int, []string) {
	quantities := make(map[string]int)
	missing := make([]string, 0)
	for _, goods := range order.Goods {
		sku := strings.TrimSpace(goods.WarehouseSKU)
		if sku == "" {
			source := strings.TrimSpace(goods.SKUCode)
			if source == "" {
				source = strings.TrimSpace(goods.SellerSKU)
			}
			if source == "" {
				source = "未知商品"
			}
			missing = appendUnique(missing, source)
			continue
		}
		quantity := 1
		if raw := strings.TrimSpace(goods.WarehouseQuantity); raw != "" {
			if parsed, parseErr := strconv.ParseFloat(raw, 64); parseErr == nil && parsed > 0 {
				quantity = int(math.Ceil(parsed))
			}
		}
		quantities[sku] += quantity
	}
	return quantities, missing
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func writeXLWMSError(writer http.ResponseWriter, err error) {
	var gateway *xlwms.GatewayError
	if errors.As(err, &gateway) {
		status := gateway.StatusCode
		if status < 400 || status >= 500 {
			status = http.StatusBadGateway
		}
		writeJSON(writer, status, response{Success: false, Error: gateway.Message})
		return
	}
	writeJSON(writer, http.StatusBadGateway, response{Success: false, Error: "领星查询失败"})
}
