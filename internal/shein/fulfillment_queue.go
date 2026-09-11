package shein

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"shein-api-manager/internal/xlwms"

	"github.com/jackc/pgx/v5"
)

type PackageSpec struct {
	LengthCM string `json:"length_cm"`
	WidthCM  string `json:"width_cm"`
	HeightCM string `json:"height_cm"`
	WeightKG string `json:"weight_kg"`
}

func (spec PackageSpec) Complete() bool {
	return positiveDecimal(spec.LengthCM) && positiveDecimal(spec.WidthCM) &&
		positiveDecimal(spec.HeightCM) && positiveDecimal(spec.WeightKG)
}

type QueueGoods struct {
	GoodsID           string                 `json:"goods_id"`
	SKUCode           string                 `json:"sku_code"`
	SellerSKU         string                 `json:"seller_sku"`
	GoodsSN           string                 `json:"goods_sn"`
	Title             string                 `json:"title"`
	Quantity          int                    `json:"quantity"`
	WarehouseSKU      string                 `json:"warehouse_sku,omitempty"`
	WarehouseQuantity string                 `json:"warehouse_quantity,omitempty"`
	WarehouseItems    []WarehouseMappingItem `json:"warehouse_items,omitempty"`
}

type WarehouseMappingItem struct {
	WarehouseSKU string      `json:"warehouse_sku"`
	Quantity     int         `json:"quantity"`
	ProductName  string      `json:"product_name,omitempty"`
	Spec         PackageSpec `json:"package_spec"`
}

type AutoFulfillmentJob struct {
	ID                   int64      `json:"id"`
	OrderNo              string     `json:"order_no"`
	Status               string     `json:"status"`
	CurrentStep          string     `json:"current_step"`
	Attempts             int        `json:"attempts"`
	SheinSKU             string     `json:"shein_sku,omitempty"`
	WarehouseSKU         string     `json:"warehouse_sku,omitempty"`
	WarehouseAddressCode string     `json:"warehouse_address_code,omitempty"`
	PreRequestID         string     `json:"pre_request_id,omitempty"`
	ExpressChannelCode   string     `json:"express_channel_code,omitempty"`
	PerformanceCost      string     `json:"performance_cost,omitempty"`
	CurrencyCode         string     `json:"currency_code,omitempty"`
	PlaceRequestID       string     `json:"place_request_id,omitempty"`
	DeliveryNo           string     `json:"delivery_no,omitempty"`
	ErrorCode            string     `json:"error_code,omitempty"`
	ErrorMessage         string     `json:"error_message,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	StartedAt            *time.Time `json:"started_at,omitempty"`
	CompletedAt          *time.Time `json:"completed_at,omitempty"`
	OutboundOrderNo      string     `json:"outbound_order_no,omitempty"`
	OutboundStatus       *int       `json:"outbound_status,omitempty"`
	OutboundStatusName   string     `json:"outbound_status_name,omitempty"`
	OMSAccount           string     `json:"oms_account,omitempty"`
	OMSOrderNo           string     `json:"oms_order_no,omitempty"`
	OMSStatusCode        *int       `json:"oms_status_code,omitempty"`
	OMSStatusKey         string     `json:"oms_status_key,omitempty"`
	OMSStatusText        string     `json:"oms_status_text,omitempty"`
	OMSWarehouseCode     string     `json:"oms_warehouse_code,omitempty"`
	OMSSyncStatus        string     `json:"oms_sync_status,omitempty"`
	OMSSyncMessage       string     `json:"oms_sync_message,omitempty"`
	OMSQueriedAt         *time.Time `json:"oms_queried_at,omitempty"`
	ParcelComplete       bool       `json:"parcel_complete,omitempty"`
}

type InventoryCheck struct {
	SourceDetailFetchedAt time.Time `json:"source_detail_fetched_at"`
	Status                string    `json:"status"`
	Categories            []string  `json:"categories"`
	ReasonDetails         []string  `json:"reason_details"`
	ErrorMessage          string    `json:"error_message,omitempty"`
	CheckedAt             time.Time `json:"checked_at"`
}

type OrderQueueItem struct {
	OrderNo            string              `json:"order_no"`
	OrderStatus        string              `json:"order_status"`
	ItemCount          int                 `json:"item_count"`
	Goods              []QueueGoods        `json:"goods"`
	Detail             map[string]any      `json:"detail"`
	AutoEligible       bool                `json:"auto_eligible"`
	ManualReasons      []string            `json:"manual_reasons"`
	SheinSKU           string              `json:"shein_sku,omitempty"`
	WarehouseSKU       string              `json:"warehouse_sku,omitempty"`
	PackageSpec        *PackageSpec        `json:"package_spec,omitempty"`
	Job                *AutoFulfillmentJob `json:"auto_fulfillment,omitempty"`
	InventoryCheck     *InventoryCheck     `json:"inventory_check,omitempty"`
	LastSeenAt         time.Time           `json:"last_seen_at"`
	DetailFetchedAt    time.Time           `json:"-"`
	staticAutoEligible bool
}

type OrderSnapshot struct {
	OrderNo    string
	Status     string
	ListData   map[string]any
	DetailData map[string]any
}

type packageMapping struct {
	SheinSKU     string
	WarehouseSKU string
	WarehouseQty string
	MappingCount int
	Spec         PackageSpec
	Items        []WarehouseMappingItem
}

func (s *Store) UpsertOrderSnapshots(ctx context.Context, shopKey string, snapshots []OrderSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin SHEIN order snapshot sync: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, snapshot := range snapshots {
		orderNo := strings.TrimSpace(snapshot.OrderNo)
		if orderNo == "" || snapshot.DetailData == nil {
			continue
		}
		status := strings.TrimSpace(snapshot.Status)
		listData := snapshot.ListData
		if listData == nil {
			listData = snapshot.DetailData
		}
		listJSON, err := json.Marshal(listData)
		if err != nil {
			return fmt.Errorf("encode SHEIN order list snapshot: %w", err)
		}
		detailJSON, err := json.Marshal(snapshot.DetailData)
		if err != nil {
			return fmt.Errorf("encode SHEIN order detail snapshot: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO shein_orders (
				shop_key, order_no, order_status, order_status_normalized,
				list_payload, detail_payload, detail_fetched_at, last_seen_at
			) VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, now(), now())
			ON CONFLICT (shop_key, order_no) DO UPDATE SET
				order_status = EXCLUDED.order_status,
				order_status_normalized = EXCLUDED.order_status_normalized,
				list_payload = CASE
					WHEN $7 THEN EXCLUDED.list_payload ELSE shein_orders.list_payload END,
				detail_payload = EXCLUDED.detail_payload,
				detail_fetched_at = now(),
				last_seen_at = now()
		`, shopKey, orderNo, status, NormalizeOrderStatus(status), listJSON, detailJSON, snapshot.ListData != nil)
		if err != nil {
			return fmt.Errorf("upsert SHEIN order snapshot: %w", err)
		}
		for _, alias := range orderSKUAliases(snapshot.DetailData) {
			if _, err := tx.Exec(ctx, `
				INSERT INTO shein_sku_aliases(shop_key,sku_code,seller_sku,source)
				VALUES($1,$2,$3,'order')
				ON CONFLICT(shop_key,sku_code,seller_sku) DO UPDATE
				SET source='order',last_seen_at=now()
			`, shopKey, alias[0], alias[1]); err != nil {
				return fmt.Errorf("upsert SHEIN order SKU alias: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit SHEIN order snapshot sync: %w", err)
	}
	return nil
}

func orderSKUAliases(detail map[string]any) [][2]string {
	aliases := make([][2]string, 0)
	seen := make(map[[2]string]struct{})
	for _, goods := range queueGoods(detail) {
		alias := [2]string{strings.TrimSpace(goods.SKUCode), strings.TrimSpace(goods.SellerSKU)}
		if alias[0] == "" || alias[1] == "" {
			continue
		}
		if _, exists := seen[alias]; exists {
			continue
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	return aliases
}

func NormalizeOrderStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "1", "pending_processing":
		return "pending_processing"
	case "2", "pending_shipping":
		return "pending_shipping"
	case "4", "shipped":
		return "shipped"
	case "5", "delivered":
		return "delivered"
	case "7", "pending_pickup":
		return "pending_pickup"
	case "6", "refunded":
		return "refunded"
	default:
		return "unknown"
	}
}

func OrderFulfilledOnPlatform(status string) bool {
	switch NormalizeOrderStatus(status) {
	case "shipped", "delivered":
		return true
	default:
		return false
	}
}

func LabelPrintable(task FulfillmentTask) bool {
	if OrderFulfilledOnPlatform(task.OrderStatusNormalized) || OrderFulfilledOnPlatform(task.OrderStatus) || OrderFulfilledOnPlatform(task.OMSStatusKey) {
		return false
	}
	if task.OMSSyncStatus == "verified" && OrderFulfilledOnPlatform(firstNonEmptyStatus(task.OMSStatusKey, task.OMSStatusText)) {
		return false
	}
	return true
}

func firstNonEmptyStatus(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *Store) MappedOrderGoods(ctx context.Context, shopKey, orderNo string) ([]QueueGoods, error) {
	detail, err := s.OrderDetail(ctx, shopKey, orderNo)
	if err != nil {
		return nil, err
	}
	goods := queueGoods(detail)
	mappings, err := s.packageMappings(ctx, shopKey, goods)
	if err != nil {
		return nil, err
	}
	for index := range goods {
		mapping, ok := mappings[goodsMappingKey(goods[index])]
		if !ok || mapping.MappingCount != 1 {
			continue
		}
		applyGoodsMapping(&goods[index], mapping)
	}
	if job, jobErr := s.GetAutoJob(ctx, shopKey, orderNo); jobErr == nil && job.WarehouseSKU != "" && len(goods) == 1 && goods[0].WarehouseSKU == "" {
		goods[0].WarehouseSKU = job.WarehouseSKU
		if goods[0].WarehouseQuantity == "" {
			goods[0].WarehouseQuantity = "1"
		}
	}
	return goods, nil
}

func (s *Store) ListPendingOrderNos(ctx context.Context, shopKey string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT order_no FROM shein_orders
		WHERE shop_key = $1 AND order_status IN ('1', '2')
		ORDER BY last_seen_at DESC
	`, shopKey)
	if err != nil {
		return nil, fmt.Errorf("list SHEIN pending order numbers: %w", err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan SHEIN pending order number: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) ListOrderQueue(ctx context.Context, shopKey, queue string) ([]OrderQueueItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.order_no, COALESCE(o.order_status, ''), o.detail_payload,
			o.last_seen_at, o.detail_fetched_at,
			j.id, j.order_no, j.status, j.current_step, j.attempts,
			j.shein_sku, j.warehouse_sku, j.warehouse_address_code,
			j.pre_request_id, j.express_channel_code,
			COALESCE(j.performance_cost::text, ''), j.currency_code,
			j.place_request_id, j.delivery_no, j.error_code, j.error_message,
			j.created_at, j.updated_at, j.started_at, j.completed_at,
			c.source_detail_fetched_at, c.status, c.categories, c.reason_details,
			c.error_message, c.checked_at
		FROM shein_orders o
		LEFT JOIN shein_go_auto_fulfillment_jobs j
			ON j.shop_key = o.shop_key AND j.order_no = o.order_no
		LEFT JOIN shein_go_order_inventory_checks c
			ON c.shop_key = o.shop_key AND c.order_no = o.order_no
		WHERE o.shop_key = $1 AND o.order_status IN ('1', '2')
		ORDER BY NULLIF(o.detail_payload->>'needDeliveryTime', '') NULLS LAST,
			o.last_seen_at DESC
	`, shopKey)
	if err != nil {
		return nil, fmt.Errorf("list SHEIN fulfillment queue: %w", err)
	}
	defer rows.Close()

	items := make([]OrderQueueItem, 0)
	allGoods := make([]QueueGoods, 0)
	for rows.Next() {
		var item OrderQueueItem
		var detailJSON []byte
		var job AutoFulfillmentJob
		var jobID *int64
		var jobOrder, jobStatus, jobStep, jobSheinSKU, jobWarehouseSKU *string
		var jobWarehouse, jobPreRequest, jobChannel, jobCost, jobCurrency *string
		var jobPlace, jobDelivery, jobErrorCode, jobErrorMessage *string
		var jobAttempts *int
		var jobCreated, jobUpdated *time.Time
		var check InventoryCheck
		var checkSource *time.Time
		var checkStatus, checkError *string
		var checkCategories, checkReasons *[]string
		var checkCheckedAt *time.Time
		err := rows.Scan(
			&item.OrderNo, &item.OrderStatus, &detailJSON, &item.LastSeenAt, &item.DetailFetchedAt,
			&jobID, &jobOrder, &jobStatus, &jobStep, &jobAttempts,
			&jobSheinSKU, &jobWarehouseSKU, &jobWarehouse,
			&jobPreRequest, &jobChannel, &jobCost, &jobCurrency,
			&jobPlace, &jobDelivery, &jobErrorCode, &jobErrorMessage,
			&jobCreated, &jobUpdated, &job.StartedAt, &job.CompletedAt,
			&checkSource, &checkStatus, &checkCategories, &checkReasons,
			&checkError, &checkCheckedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan SHEIN fulfillment queue: %w", err)
		}
		if err := json.Unmarshal(detailJSON, &item.Detail); err != nil {
			return nil, fmt.Errorf("decode SHEIN order detail snapshot: %w", err)
		}
		item.Goods = queueGoods(item.Detail)
		item.ItemCount = len(item.Goods)
		allGoods = append(allGoods, item.Goods...)
		if jobID != nil {
			job.ID, job.OrderNo = *jobID, pointerValue(jobOrder)
			job.Status, job.CurrentStep, job.Attempts = pointerValue(jobStatus), pointerValue(jobStep), pointerInt(jobAttempts)
			job.SheinSKU, job.WarehouseSKU = pointerValue(jobSheinSKU), pointerValue(jobWarehouseSKU)
			job.WarehouseAddressCode, job.PreRequestID = pointerValue(jobWarehouse), pointerValue(jobPreRequest)
			job.ExpressChannelCode, job.PerformanceCost, job.CurrencyCode = pointerValue(jobChannel), pointerValue(jobCost), pointerValue(jobCurrency)
			job.PlaceRequestID, job.DeliveryNo = pointerValue(jobPlace), pointerValue(jobDelivery)
			job.ErrorCode, job.ErrorMessage = pointerValue(jobErrorCode), pointerValue(jobErrorMessage)
			job.CreatedAt, job.UpdatedAt = pointerTime(jobCreated), pointerTime(jobUpdated)
			item.Job = &job
		}
		if checkSource != nil {
			check.SourceDetailFetchedAt = *checkSource
			check.Status, check.ErrorMessage = pointerValue(checkStatus), pointerValue(checkError)
			if checkCategories != nil {
				check.Categories = *checkCategories
			}
			if checkReasons != nil {
				check.ReasonDetails = *checkReasons
			}
			if checkCheckedAt != nil {
				check.CheckedAt = *checkCheckedAt
			}
			item.InventoryCheck = &check
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read SHEIN fulfillment queue: %w", err)
	}

	mappings, err := s.packageMappings(ctx, shopKey, allGoods)
	if err != nil {
		return nil, err
	}
	filtered := make([]OrderQueueItem, 0, len(items))
	for index := range items {
		classifyOrderQueueItem(&items[index], mappings)
		switch queue {
		case "pending":
			if items[index].CanRunAutomaticFulfillment() {
				filtered = append(filtered, items[index])
			}
		case "manual":
			if !items[index].AutoEligible {
				filtered = append(filtered, items[index])
			}
		case "all", "":
			filtered = append(filtered, items[index])
		default:
			return nil, errors.New("unknown SHEIN fulfillment queue")
		}
	}
	return filtered, nil
}

func (s *Store) packageMappings(ctx context.Context, shopKey string, goods []QueueGoods) (map[string]packageMapping, error) {
	if len(goods) == 0 {
		return map[string]packageMapping{}, nil
	}
	if s.platformSKUResolver == nil {
		return nil, errors.New("XLWMS platform SKU resolver is not configured")
	}
	missingSellerCodes := make([]string, 0)
	seenCodes := make(map[string]struct{})
	for _, item := range goods {
		if strings.TrimSpace(item.SellerSKU) != "" {
			continue
		}
		code := strings.TrimSpace(item.SKUCode)
		if code == "" {
			continue
		}
		if _, exists := seenCodes[code]; !exists {
			seenCodes[code] = struct{}{}
			missingSellerCodes = append(missingSellerCodes, code)
		}
	}
	aliases := make(map[string][]string)
	if len(missingSellerCodes) > 0 {
		rows, err := s.pool.Query(ctx, `
			SELECT sku_code,seller_sku FROM shein_sku_aliases
			WHERE shop_key=$1 AND sku_code=ANY($2::text[])
			ORDER BY sku_code,CASE source WHEN 'order' THEN 0 ELSE 1 END,last_seen_at DESC,seller_sku
		`, shopKey, missingSellerCodes)
		if err != nil {
			return nil, fmt.Errorf("load SHEIN SKU aliases: %w", err)
		}
		for rows.Next() {
			var code, sellerSKU string
			if err := rows.Scan(&code, &sellerSKU); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan SHEIN SKU alias: %w", err)
			}
			aliases[code] = append(aliases[code], sellerSKU)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read SHEIN SKU aliases: %w", err)
		}
		rows.Close()
	}
	candidatesByKey := make(map[string][]string)
	allPlatformSKUs := make([]string, 0)
	seenPlatformSKUs := make(map[string]struct{})
	for _, item := range goods {
		key := goodsMappingKey(item)
		if key == "" {
			continue
		}
		candidates := platformSKUCandidates(item, aliases)
		candidatesByKey[key] = candidates
		for _, candidate := range candidates {
			if _, exists := seenPlatformSKUs[candidate]; exists {
				continue
			}
			seenPlatformSKUs[candidate] = struct{}{}
			allPlatformSKUs = append(allPlatformSKUs, candidate)
		}
	}
	resolved := make(map[string]packageMapping)
	for start := 0; start < len(allPlatformSKUs); start += 500 {
		end := start + 500
		if end > len(allPlatformSKUs) {
			end = len(allPlatformSKUs)
		}
		resolution, err := s.platformSKUResolver.ResolvePlatformSKUs(ctx, "shein", allPlatformSKUs[start:end])
		if err != nil {
			return nil, fmt.Errorf("resolve SHEIN platform SKU through XLWMS: %w", err)
		}
		for _, mapping := range resolution.Mappings {
			resolved[mapping.PlatformSKU] = packageMappingFromXLWMS(mapping)
		}
	}
	result := make(map[string]packageMapping)
	for key, candidates := range candidatesByKey {
		var selected packageMapping
		for _, candidate := range candidates {
			mapping, exists := resolved[candidate]
			if !exists {
				continue
			}
			if selected.MappingCount == 0 {
				selected = mapping
				continue
			}
			if !samePackageRecipe(selected, mapping) {
				selected.MappingCount = 2
				break
			}
		}
		if selected.MappingCount > 0 {
			result[key] = selected
		}
	}
	return result, nil
}

func platformSKUCandidates(goods QueueGoods, aliases map[string][]string) []string {
	code := strings.TrimSpace(goods.SKUCode)
	candidates := append([]string(nil), aliases[code]...)
	if sellerSKU := strings.TrimSpace(goods.SellerSKU); sellerSKU != "" {
		candidates = []string{sellerSKU}
	}
	if code != "" {
		candidates = append(candidates, code)
	}
	result := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

func packageMappingFromXLWMS(mapping xlwms.PlatformSKUMapping) packageMapping {
	result := packageMapping{SheinSKU: mapping.PlatformSKU, MappingCount: 1, Items: make([]WarehouseMappingItem, 0, len(mapping.Items))}
	for _, item := range mapping.Items {
		result.Items = append(result.Items, WarehouseMappingItem{
			WarehouseSKU: item.WarehouseSKU, Quantity: item.Quantity, ProductName: item.ProductName,
			Spec: PackageSpec{LengthCM: decimalString(item.LengthCM), WidthCM: decimalString(item.WidthCM), HeightCM: decimalString(item.HeightCM), WeightKG: decimalString(item.WeightKG)},
		})
	}
	if len(result.Items) == 1 {
		result.WarehouseSKU = result.Items[0].WarehouseSKU
		result.WarehouseQty = strconv.Itoa(result.Items[0].Quantity)
		result.Spec = result.Items[0].Spec
	}
	return result
}

func decimalString(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func samePackageRecipe(left, right packageMapping) bool {
	if len(left.Items) != len(right.Items) {
		return false
	}
	for index := range left.Items {
		if left.Items[index].WarehouseSKU != right.Items[index].WarehouseSKU || left.Items[index].Quantity != right.Items[index].Quantity {
			return false
		}
	}
	return true
}

func goodsMappingKey(goods QueueGoods) string {
	if sellerSKU := strings.TrimSpace(goods.SellerSKU); sellerSKU != "" {
		return "seller:" + sellerSKU
	}
	if skuCode := strings.TrimSpace(goods.SKUCode); skuCode != "" {
		return "code:" + skuCode
	}
	return ""
}

func applyGoodsMapping(goods *QueueGoods, mapping packageMapping) {
	goods.WarehouseItems = append([]WarehouseMappingItem(nil), mapping.Items...)
	if len(mapping.Items) == 1 {
		goods.WarehouseSKU = mapping.WarehouseSKU
		goods.WarehouseQuantity = mapping.WarehouseQty
	}
}

func queueGoods(detail map[string]any) []QueueGoods {
	raw, ok := detail["orderGoodsInfoList"].([]any)
	if !ok {
		return nil
	}
	goods := make([]QueueGoods, 0, len(raw))
	for _, value := range raw {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		goods = append(goods, QueueGoods{
			GoodsID: textValue(entry["goodsId"]), SKUCode: textValue(entry["skuCode"]),
			SellerSKU: textValue(entry["sellerSku"]), GoodsSN: textValue(entry["goodsSn"]),
			Title: textValue(entry["goodsTitle"]), Quantity: goodsQuantity(entry),
		})
	}
	return goods
}

func goodsQuantity(entry map[string]any) int {
	for _, key := range []string{"quantity", "goodsQuantity", "qty", "skuQuantity", "newGoodsNumber"} {
		if parsed, err := strconv.Atoi(textValue(entry[key])); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 1
}

func orderGoodsUnits(goods []QueueGoods) int {
	total := 0
	for _, item := range goods {
		if item.Quantity > 0 {
			total += item.Quantity
			continue
		}
		total++
	}
	return total
}

func classifyOrderQueueItem(item *OrderQueueItem, mappings map[string]packageMapping) {
	reasons := make([]string, 0)
	for index := range item.Goods {
		mapping, ok := mappings[goodsMappingKey(item.Goods[index])]
		if ok && mapping.MappingCount == 1 {
			applyGoodsMapping(&item.Goods[index], mapping)
		}
	}
	units := orderGoodsUnits(item.Goods)
	if item.ItemCount == 0 {
		reasons = append(reasons, "订单详情缺少商品明细")
	} else if item.ItemCount > 1 || units > 1 {
		reasons = append(reasons, "多件订单需人工确认包裹")
	}
	if !supportsIntegrated(item.Detail) {
		reasons = append(reasons, "订单不支持 SHEIN 集成物流")
	}
	if textValue(item.Detail["orderType"]) == "5" {
		reasons = append(reasons, "认证仓订单不可在线履约")
	}
	if printStatus := textValue(item.Detail["printOrderStatus"]); printStatus != "" && printStatus != "1" {
		reasons = append(reasons, "平台标记订单暂不可处理")
	}
	if item.ItemCount == 1 && units == 1 {
		item.SheinSKU = strings.TrimSpace(item.Goods[0].SellerSKU)
		if item.SheinSKU == "" {
			item.SheinSKU = strings.TrimSpace(item.Goods[0].SKUCode)
		}
		mapping, ok := mappings[goodsMappingKey(item.Goods[0])]
		switch {
		case item.SheinSKU == "":
			reasons = append(reasons, "商品缺少 sellerSku 和 skuCode")
		case !ok:
			reasons = append(reasons, "SKU 未绑定仓库商品")
		case mapping.MappingCount > 1:
			reasons = append(reasons, "SKU 对应多个仓库商品")
		case len(mapping.Items) != 1:
			reasons = append(reasons, "组合商品需人工确认包裹")
		default:
			item.WarehouseSKU = mapping.WarehouseSKU
			spec := mapping.Spec
			item.PackageSpec = &spec
			if !spec.Complete() {
				reasons = append(reasons, "仓库商品尺寸或重量不完整")
			}
		}
	}
	item.ManualReasons = reasons
	item.AutoEligible = len(reasons) == 0
	item.staticAutoEligible = item.AutoEligible
	applyInventoryCheck(item)
}

func (item OrderQueueItem) hasCurrentInventoryCheck() bool {
	return item.InventoryCheck != nil && !item.DetailFetchedAt.IsZero() &&
		item.InventoryCheck.SourceDetailFetchedAt.Equal(item.DetailFetchedAt)
}

func (item OrderQueueItem) ReadyForAutomaticFulfillment() bool {
	return item.AutoEligible && item.hasCurrentInventoryCheck() && item.InventoryCheck.Status == "eligible"
}

func (item OrderQueueItem) CanRunAutomaticFulfillment() bool {
	return item.ReadyForAutomaticFulfillment() && (item.Job == nil || item.Job.Status == "failed")
}

func (item OrderQueueItem) EligibleBeforeInventoryCheck() bool {
	return item.staticAutoEligible
}

func (item OrderQueueItem) AutomaticFulfillmentReason() string {
	if len(item.ManualReasons) > 0 {
		return item.ManualReasons[0]
	}
	if !item.hasCurrentInventoryCheck() {
		return "订单尚未完成实时库存校验"
	}
	if item.InventoryCheck.ErrorMessage != "" {
		return item.InventoryCheck.ErrorMessage
	}
	return "订单不在当前可自动履约队列"
}

func applyInventoryCheck(item *OrderQueueItem) {
	if !item.staticAutoEligible || !item.hasCurrentInventoryCheck() || item.InventoryCheck.Status == "eligible" {
		return
	}
	if item.InventoryCheck.Status == "manual" {
		item.ManualReasons = append(item.ManualReasons, item.InventoryCheck.ReasonDetails...)
	} else if item.InventoryCheck.ErrorMessage != "" {
		item.ManualReasons = append(item.ManualReasons, "实时库存校验失败："+item.InventoryCheck.ErrorMessage)
	} else {
		item.ManualReasons = append(item.ManualReasons, "实时库存校验失败，请重新同步后再试")
	}
	item.AutoEligible = false
}

func (s *Store) SaveInventoryCheck(ctx context.Context, shopKey, orderNo string, check InventoryCheck) error {
	if !check.SourceDetailFetchedAt.IsZero() {
		check.SourceDetailFetchedAt = check.SourceDetailFetchedAt.UTC()
	}
	if check.Status != "eligible" && check.Status != "manual" && check.Status != "failed" {
		return errors.New("invalid SHEIN inventory check status")
	}
	if check.SourceDetailFetchedAt.IsZero() {
		return errors.New("inventory check source timestamp is required")
	}
	if check.Categories == nil {
		check.Categories = []string{}
	}
	if check.ReasonDetails == nil {
		check.ReasonDetails = []string{}
	}
	if len(check.ErrorMessage) > 500 {
		check.ErrorMessage = check.ErrorMessage[:500]
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO shein_go_order_inventory_checks (
			shop_key, order_no, source_detail_fetched_at, status,
			categories, reason_details, error_message, checked_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (shop_key, order_no) DO UPDATE SET
			source_detail_fetched_at = EXCLUDED.source_detail_fetched_at,
			status = EXCLUDED.status,
			categories = EXCLUDED.categories,
			reason_details = EXCLUDED.reason_details,
			error_message = EXCLUDED.error_message,
			checked_at = now()
	`, shopKey, orderNo, check.SourceDetailFetchedAt, check.Status,
		check.Categories, check.ReasonDetails, check.ErrorMessage)
	if err != nil {
		return fmt.Errorf("save SHEIN inventory check: %w", err)
	}
	return nil
}

func logisticsOptions(detail map[string]any) []int {
	values, ok := detail["optionalLogisticsList"].([]any)
	if !ok {
		return nil
	}
	options := make([]int, 0, len(values))
	for _, candidate := range values {
		parsed, err := strconv.Atoi(textValue(candidate))
		if err != nil {
			continue
		}
		options = append(options, parsed)
	}
	return options
}

func hasLogisticsOption(detail map[string]any, option int) bool {
	for _, candidate := range logisticsOptions(detail) {
		if candidate == option {
			return true
		}
	}
	return false
}

func supportsIntegrated(detail map[string]any) bool {
	if options := logisticsOptions(detail); len(options) > 0 {
		return hasLogisticsOption(detail, 1)
	}
	return textValue(detail["performanceType"]) == "1"
}

func supportsSelfShipping(detail map[string]any) bool {
	return hasLogisticsOption(detail, 2)
}

func CanPurchasePlatformLabel(detail map[string]any) bool {
	return supportsIntegrated(detail)
}

func IsCODOrder(detail map[string]any) bool {
	return textValue(detail["isCod"]) == "1" || textValue(detail["is_cod"]) == "1"
}

func RequiresAddressTransition(detail map[string]any, orderStatus string) bool {
	if strings.TrimSpace(orderStatus) != "1" {
		return false
	}
	return !CanPurchasePlatformLabel(detail) && supportsSelfShipping(detail)
}

func (s *Store) EnsureWarehouseLedgerJob(ctx context.Context, shopKey, orderNo, warehouseAddressCode, channelCode, placeRequestID, deliveryNo string) error {
	orderNo = strings.TrimSpace(orderNo)
	if shopKey == "" || orderNo == "" {
		return errors.New("order number is required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO shein_go_auto_fulfillment_jobs (
			shop_key, order_no, status, current_step,
			warehouse_address_code, express_channel_code, place_request_id, delivery_no
		) VALUES ($1, $2, 'completed', 'completed', $3, $4, $5, $6)
		ON CONFLICT (shop_key, order_no) DO UPDATE SET
			warehouse_address_code = COALESCE(NULLIF(EXCLUDED.warehouse_address_code, ''), shein_go_auto_fulfillment_jobs.warehouse_address_code),
			express_channel_code = COALESCE(NULLIF(EXCLUDED.express_channel_code, ''), shein_go_auto_fulfillment_jobs.express_channel_code),
			place_request_id = COALESCE(NULLIF(EXCLUDED.place_request_id, ''), shein_go_auto_fulfillment_jobs.place_request_id),
			delivery_no = COALESCE(NULLIF(EXCLUDED.delivery_no, ''), shein_go_auto_fulfillment_jobs.delivery_no),
			updated_at = now()
		WHERE shein_go_auto_fulfillment_jobs.status IN ('completed', 'failed')
	`, shopKey, orderNo, strings.TrimSpace(warehouseAddressCode), strings.TrimSpace(channelCode),
		strings.TrimSpace(placeRequestID), strings.TrimSpace(deliveryNo))
	if err != nil {
		return fmt.Errorf("ensure SHEIN warehouse ledger job: %w", err)
	}
	return nil
}

func (s *Store) EnqueueAutoJob(ctx context.Context, shopKey, orderNo string) (AutoFulfillmentJob, bool, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return AutoFulfillmentJob{}, false, errors.New("order number is required")
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO shein_go_auto_fulfillment_jobs (shop_key, order_no)
		VALUES ($1, $2)
		ON CONFLICT (shop_key, order_no) DO UPDATE SET
			status = 'queued', current_step = 'queued', error_code = '', error_message = '',
			updated_at = now(), completed_at = NULL
		WHERE shein_go_auto_fulfillment_jobs.status = 'failed'
	`, shopKey, orderNo)
	if err != nil {
		return AutoFulfillmentJob{}, false, fmt.Errorf("enqueue SHEIN auto fulfillment: %w", err)
	}
	job, err := s.GetAutoJob(ctx, shopKey, orderNo)
	return job, tag.RowsAffected() == 1, err
}

func (s *Store) ClaimAutoJob(ctx context.Context, shopKey, orderNo string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE shein_go_auto_fulfillment_jobs
		SET status = 'running', current_step = 'validating', attempts = attempts + 1,
			started_at = COALESCE(started_at, now()), updated_at = now()
		WHERE shop_key = $1 AND order_no = $2 AND status = 'queued'
	`, shopKey, orderNo)
	if err != nil {
		return false, fmt.Errorf("claim SHEIN auto fulfillment: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) SetAutoJobState(ctx context.Context, shopKey, orderNo, status, step, errorCode, errorMessage string) error {
	if len(errorMessage) > 500 {
		errorMessage = errorMessage[:500]
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE shein_go_auto_fulfillment_jobs
		SET status = $3, current_step = $4, error_code = $5, error_message = $6,
			updated_at = now(),
			completed_at = CASE WHEN $3 IN ('completed', 'failed') THEN now() ELSE NULL END
		WHERE shop_key = $1 AND order_no = $2
	`, shopKey, orderNo, status, step, errorCode, errorMessage)
	if err != nil {
		return fmt.Errorf("update SHEIN auto fulfillment state: %w", err)
	}
	return nil
}

func (s *Store) SetAutoJobSelection(ctx context.Context, shopKey, orderNo, sheinSKU, warehouseSKU,
	omsAccount, warehouseAddressCode, preRequestID, channelCode, cost, currency string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE shein_go_auto_fulfillment_jobs
		SET shein_sku = $3, warehouse_sku = $4, oms_account = $5, warehouse_address_code = $6,
			pre_request_id = $7, express_channel_code = $8,
			performance_cost = NULLIF($9, '')::numeric, currency_code = $10, updated_at = now()
		WHERE shop_key = $1 AND order_no = $2
	`, shopKey, orderNo, sheinSKU, warehouseSKU, strings.ToLower(strings.TrimSpace(omsAccount)), warehouseAddressCode,
		preRequestID, channelCode, cost, currency)
	if err != nil {
		return fmt.Errorf("save SHEIN auto fulfillment selection: %w", err)
	}
	return nil
}

func (s *Store) SetAutoJobResult(ctx context.Context, shopKey, orderNo, placeRequestID, deliveryNo string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE shein_go_auto_fulfillment_jobs
		SET place_request_id = COALESCE(NULLIF($3, ''), place_request_id),
			delivery_no = COALESCE(NULLIF($4, ''), delivery_no), updated_at = now()
		WHERE shop_key = $1 AND order_no = $2
	`, shopKey, orderNo, placeRequestID, deliveryNo)
	if err != nil {
		return fmt.Errorf("save SHEIN auto fulfillment result: %w", err)
	}
	return nil
}

func (s *Store) RejectedAutoJobCarriers(ctx context.Context, shopKey, orderNo string) ([]string, error) {
	var carriers []string
	err := s.pool.QueryRow(ctx, `
		SELECT rejected_carriers
		FROM shein_go_auto_fulfillment_jobs
		WHERE shop_key = $1 AND order_no = $2
	`, shopKey, orderNo).Scan(&carriers)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("SHEIN auto fulfillment job not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load rejected SHEIN carriers: %w", err)
	}
	return carriers, nil
}

func (s *Store) RejectAutoJobCarrier(ctx context.Context, shopKey, orderNo, carrier, reason string) error {
	carrier = strings.ToUpper(strings.TrimSpace(carrier))
	if carrier == "" {
		return errors.New("rejected carrier is required")
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 500 {
		reason = reason[:500]
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin SHEIN carrier fallback: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var preRequestID string
	err = tx.QueryRow(ctx, `
		SELECT pre_request_id
		FROM shein_go_auto_fulfillment_jobs
		WHERE shop_key = $1 AND order_no = $2
		FOR UPDATE
	`, shopKey, orderNo).Scan(&preRequestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("SHEIN auto fulfillment job not found")
	}
	if err != nil {
		return fmt.Errorf("lock SHEIN carrier fallback: %w", err)
	}
	if preRequestID != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE shein_label_purchase_choices
			SET rejected_at = now(), rejection_reason = $3
			WHERE shop_key = $1 AND pre_request_id = $2
		`, shopKey, preRequestID, reason); err != nil {
			return fmt.Errorf("mark rejected SHEIN label purchase: %w", err)
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE shein_go_auto_fulfillment_jobs
		SET rejected_carriers = CASE
				WHEN $3 = ANY(rejected_carriers) THEN rejected_carriers
				ELSE array_append(rejected_carriers, $3)
			END,
			warehouse_address_code = '', pre_request_id = '', express_channel_code = '',
			performance_cost = NULL, currency_code = '', place_request_id = '', delivery_no = '',
			current_step = 'quote_channels', updated_at = now()
		WHERE shop_key = $1 AND order_no = $2
	`, shopKey, orderNo, carrier)
	if err != nil {
		return fmt.Errorf("reject SHEIN carrier for automatic fallback: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("SHEIN auto fulfillment job not found")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit SHEIN carrier fallback: %w", err)
	}
	return nil
}

func (s *Store) RequeueWaitingAutoJob(ctx context.Context, shopKey, orderNo string, maxAttempts int) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE shein_go_auto_fulfillment_jobs
		SET status = 'queued', current_step = 'queued', updated_at = now()
		WHERE shop_key = $1 AND order_no = $2
			AND status = 'waiting_confirmation' AND attempts < $3
	`, shopKey, orderNo, maxAttempts)
	if err != nil {
		return false, fmt.Errorf("requeue SHEIN automatic fulfillment: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) GetAutoJob(ctx context.Context, shopKey, orderNo string) (AutoFulfillmentJob, error) {
	row := s.pool.QueryRow(ctx, autoJobSelect+` WHERE j.shop_key = $1 AND j.order_no = $2`, shopKey, orderNo)
	job, err := scanAutoJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutoFulfillmentJob{}, errors.New("SHEIN auto fulfillment job not found")
	}
	return job, err
}

func (s *Store) ListAutoJobs(ctx context.Context, shopKey, queue string, limit int) ([]AutoFulfillmentJob, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	filter := ""
	switch queue {
	case "processing":
		filter = " AND j.status IN ('queued', 'running', 'waiting_confirmation')"
	case "exceptions":
		filter = " AND j.status = 'failed'"
	case "all", "":
	default:
		return nil, errors.New("unknown SHEIN automatic fulfillment queue")
	}
	rows, err := s.pool.Query(ctx, autoJobSelect+` WHERE j.shop_key = $1`+filter+` ORDER BY j.updated_at DESC LIMIT $2`, shopKey, limit)
	if err != nil {
		return nil, fmt.Errorf("list SHEIN auto fulfillment jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]AutoFulfillmentJob, 0)
	for rows.Next() {
		job, err := scanAutoJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) ListResumableAutoJobs(ctx context.Context, shopKey string) ([][2]string, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE shein_go_auto_fulfillment_jobs
		SET status = 'queued', current_step = 'queued', updated_at = now()
		WHERE shop_key = $1 AND status IN ('queued', 'running', 'waiting_confirmation')
		RETURNING shop_key, order_no
	`, shopKey)
	if err != nil {
		return nil, fmt.Errorf("resume SHEIN automatic fulfillment jobs: %w", err)
	}
	defer rows.Close()
	var jobs [][2]string
	for rows.Next() {
		var job [2]string
		if err := rows.Scan(&job[0], &job[1]); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

const autoJobSelect = `SELECT j.id, j.order_no, j.status, j.current_step, j.attempts,
	j.shein_sku, j.warehouse_sku, j.warehouse_address_code, j.pre_request_id,
	j.express_channel_code, COALESCE(j.performance_cost::text, ''), j.currency_code,
	j.place_request_id, j.delivery_no, j.error_code, j.error_message,
	j.created_at, j.updated_at, j.started_at, j.completed_at,
	COALESCE(t.outbound_order_no, ''), t.outbound_status, COALESCE(t.outbound_status_name, ''),
	COALESCE(NULLIF(j.oms_account, ''), t.oms_account, ''), COALESCE(t.oms_order_no, ''), t.oms_status_code,
	COALESCE(t.oms_status_key, ''), COALESCE(t.oms_status_text, ''),
	COALESCE(t.oms_warehouse_code, ''), COALESCE(t.oms_sync_status, ''),
	COALESCE(t.oms_sync_message, ''), t.oms_queried_at, COALESCE(t.label_attached, false)
	FROM shein_go_auto_fulfillment_jobs j
	LEFT JOIN LATERAL (
		SELECT outbound_order_no, outbound_status, outbound_status_name, label_attached,
			oms_account, oms_order_no, oms_status_code, oms_status_key, oms_status_text,
			oms_warehouse_code, oms_sync_status, oms_sync_message, oms_queried_at
		FROM shein_go_fulfillment_tasks
		WHERE shop_key = j.shop_key AND order_no = j.order_no
		ORDER BY updated_at DESC
		LIMIT 1
	) t ON true`

type autoJobScanner interface {
	Scan(dest ...any) error
}

func scanAutoJob(scanner autoJobScanner) (AutoFulfillmentJob, error) {
	var job AutoFulfillmentJob
	var labelAttached bool
	err := scanner.Scan(&job.ID, &job.OrderNo, &job.Status, &job.CurrentStep, &job.Attempts,
		&job.SheinSKU, &job.WarehouseSKU, &job.WarehouseAddressCode, &job.PreRequestID,
		&job.ExpressChannelCode, &job.PerformanceCost, &job.CurrencyCode,
		&job.PlaceRequestID, &job.DeliveryNo, &job.ErrorCode, &job.ErrorMessage,
		&job.CreatedAt, &job.UpdatedAt, &job.StartedAt, &job.CompletedAt,
		&job.OutboundOrderNo, &job.OutboundStatus, &job.OutboundStatusName,
		&job.OMSAccount, &job.OMSOrderNo, &job.OMSStatusCode, &job.OMSStatusKey, &job.OMSStatusText,
		&job.OMSWarehouseCode, &job.OMSSyncStatus, &job.OMSSyncMessage, &job.OMSQueriedAt, &labelAttached)
	if err != nil {
		return AutoFulfillmentJob{}, fmt.Errorf("scan SHEIN automatic fulfillment job: %w", err)
	}
	job.ParcelComplete = job.OutboundOrderNo != "" && labelAttached &&
		job.OutboundStatus != nil && (*job.OutboundStatus == 0 || *job.OutboundStatus == 1 || *job.OutboundStatus == 2 || *job.OutboundStatus == 3)
	return job, nil
}

func jobIsActive(status string) bool {
	return status == "queued" || status == "running" || status == "waiting_confirmation"
}

func pointerValue(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

func pointerInt(pointer *int) int {
	if pointer == nil {
		return 0
	}
	return *pointer
}

func pointerTime(pointer *time.Time) time.Time {
	if pointer == nil {
		return time.Time{}
	}
	return *pointer
}

func textValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func positiveDecimal(value string) bool {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil && parsed > 0
}
