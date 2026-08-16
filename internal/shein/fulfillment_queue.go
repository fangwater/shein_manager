package shein

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	GoodsID           string `json:"goods_id"`
	SKUCode           string `json:"sku_code"`
	SellerSKU         string `json:"seller_sku"`
	GoodsSN           string `json:"goods_sn"`
	Title             string `json:"title"`
	WarehouseSKU      string `json:"warehouse_sku,omitempty"`
	WarehouseQuantity string `json:"warehouse_quantity,omitempty"`
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
}

type OrderQueueItem struct {
	OrderNo       string              `json:"order_no"`
	OrderStatus   string              `json:"order_status"`
	ItemCount     int                 `json:"item_count"`
	Goods         []QueueGoods        `json:"goods"`
	Detail        map[string]any      `json:"detail"`
	AutoEligible  bool                `json:"auto_eligible"`
	ManualReasons []string            `json:"manual_reasons"`
	SheinSKU      string              `json:"shein_sku,omitempty"`
	WarehouseSKU  string              `json:"warehouse_sku,omitempty"`
	PackageSpec   *PackageSpec        `json:"package_spec,omitempty"`
	Job           *AutoFulfillmentJob `json:"auto_fulfillment,omitempty"`
	LastSeenAt    time.Time           `json:"last_seen_at"`
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
		`, shopKey, orderNo, status, normalizeOrderStatus(status), listJSON, detailJSON, snapshot.ListData != nil)
		if err != nil {
			return fmt.Errorf("upsert SHEIN order snapshot: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit SHEIN order snapshot sync: %w", err)
	}
	return nil
}

func normalizeOrderStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "1":
		return "pending_processing"
	case "2":
		return "pending_shipping"
	case "4", "7":
		return "shipped"
	case "5":
		return "delivered"
	case "6":
		return "refunded"
	default:
		return "unknown"
	}
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
			o.last_seen_at,
			j.id, j.order_no, j.status, j.current_step, j.attempts,
			j.shein_sku, j.warehouse_sku, j.warehouse_address_code,
			j.pre_request_id, j.express_channel_code,
			COALESCE(j.performance_cost::text, ''), j.currency_code,
			j.place_request_id, j.delivery_no, j.error_code, j.error_message,
			j.created_at, j.updated_at, j.started_at, j.completed_at
		FROM shein_orders o
		LEFT JOIN shein_go_auto_fulfillment_jobs j
			ON j.shop_key = o.shop_key AND j.order_no = o.order_no
		WHERE o.shop_key = $1 AND o.order_status IN ('1', '2')
		ORDER BY NULLIF(o.detail_payload->>'needDeliveryTime', '') NULLS LAST,
			o.last_seen_at DESC
	`, shopKey)
	if err != nil {
		return nil, fmt.Errorf("list SHEIN fulfillment queue: %w", err)
	}
	defer rows.Close()

	items := make([]OrderQueueItem, 0)
	allSKUs := make([]string, 0)
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
		err := rows.Scan(
			&item.OrderNo, &item.OrderStatus, &detailJSON, &item.LastSeenAt,
			&jobID, &jobOrder, &jobStatus, &jobStep, &jobAttempts,
			&jobSheinSKU, &jobWarehouseSKU, &jobWarehouse,
			&jobPreRequest, &jobChannel, &jobCost, &jobCurrency,
			&jobPlace, &jobDelivery, &jobErrorCode, &jobErrorMessage,
			&jobCreated, &jobUpdated, &job.StartedAt, &job.CompletedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan SHEIN fulfillment queue: %w", err)
		}
		if err := json.Unmarshal(detailJSON, &item.Detail); err != nil {
			return nil, fmt.Errorf("decode SHEIN order detail snapshot: %w", err)
		}
		item.Goods = queueGoods(item.Detail)
		item.ItemCount = len(item.Goods)
		for _, goods := range item.Goods {
			if goods.SKUCode != "" {
				allSKUs = append(allSKUs, goods.SKUCode)
			}
		}
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
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read SHEIN fulfillment queue: %w", err)
	}

	mappings, err := s.packageMappings(ctx, shopKey, allSKUs)
	if err != nil {
		return nil, err
	}
	filtered := make([]OrderQueueItem, 0, len(items))
	for index := range items {
		classifyOrderQueueItem(&items[index], mappings)
		switch queue {
		case "pending":
			if items[index].AutoEligible && items[index].Job == nil {
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

func (s *Store) packageMappings(ctx context.Context, shopKey string, skus []string) (map[string]packageMapping, error) {
	if len(skus) == 0 {
		return map[string]packageMapping{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH counts AS (
			SELECT shein_sku, COUNT(DISTINCT warehouse_sku) AS mapping_count
			FROM shein_sku_mappings
			WHERE shop_key = $1 AND enabled = true AND shein_sku = ANY($2::text[])
			GROUP BY shein_sku
		), ranked AS (
			SELECT m.shein_sku, m.warehouse_sku, m.warehouse_qty::text,
				counts.mapping_count,
				w.length_cm::text, w.width_cm::text, w.height_cm::text, w.weight_kg::text,
				ROW_NUMBER() OVER (PARTITION BY m.shein_sku ORDER BY m.updated_at DESC, m.id DESC) AS rank
			FROM shein_sku_mappings m
			JOIN counts USING (shein_sku)
			LEFT JOIN shein_warehouse_skus w
				ON w.shop_key = m.shop_key AND w.warehouse_sku = m.warehouse_sku AND w.enabled = true
			WHERE m.shop_key = $1 AND m.enabled = true
		)
		SELECT shein_sku, warehouse_sku, warehouse_qty, mapping_count,
			COALESCE(length_cm, ''), COALESCE(width_cm, ''),
			COALESCE(height_cm, ''), COALESCE(weight_kg, '')
		FROM ranked WHERE rank = 1
	`, shopKey, skus)
	if err != nil {
		return nil, fmt.Errorf("load SHEIN package mappings: %w", err)
	}
	defer rows.Close()
	result := make(map[string]packageMapping)
	for rows.Next() {
		var mapping packageMapping
		if err := rows.Scan(&mapping.SheinSKU, &mapping.WarehouseSKU, &mapping.WarehouseQty, &mapping.MappingCount,
			&mapping.Spec.LengthCM, &mapping.Spec.WidthCM, &mapping.Spec.HeightCM, &mapping.Spec.WeightKG); err != nil {
			return nil, fmt.Errorf("scan SHEIN package mapping: %w", err)
		}
		result[mapping.SheinSKU] = mapping
	}
	return result, rows.Err()
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
			Title: textValue(entry["goodsTitle"]),
		})
	}
	return goods
}

func classifyOrderQueueItem(item *OrderQueueItem, mappings map[string]packageMapping) {
	reasons := make([]string, 0)
	for index := range item.Goods {
		mapping, ok := mappings[item.Goods[index].SKUCode]
		if ok && mapping.MappingCount == 1 {
			item.Goods[index].WarehouseSKU = mapping.WarehouseSKU
			item.Goods[index].WarehouseQuantity = mapping.WarehouseQty
		}
	}
	if item.ItemCount == 0 {
		reasons = append(reasons, "订单详情缺少商品明细")
	} else if item.ItemCount > 1 {
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
	if item.ItemCount == 1 {
		item.SheinSKU = item.Goods[0].SKUCode
		mapping, ok := mappings[item.SheinSKU]
		switch {
		case item.SheinSKU == "":
			reasons = append(reasons, "商品缺少 skuCode")
		case !ok:
			reasons = append(reasons, "SKU 未绑定仓库商品")
		case mapping.MappingCount > 1:
			reasons = append(reasons, "SKU 对应多个仓库商品")
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

func RequiresAddressTransition(detail map[string]any, orderStatus string) bool {
	if strings.TrimSpace(orderStatus) != "1" {
		return false
	}
	return !CanPurchasePlatformLabel(detail) && supportsSelfShipping(detail)
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
	warehouseAddressCode, preRequestID, channelCode, cost, currency string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE shein_go_auto_fulfillment_jobs
		SET shein_sku = $3, warehouse_sku = $4, warehouse_address_code = $5,
			pre_request_id = $6, express_channel_code = $7,
			performance_cost = NULLIF($8, '')::numeric, currency_code = $9, updated_at = now()
		WHERE shop_key = $1 AND order_no = $2
	`, shopKey, orderNo, sheinSKU, warehouseSKU, warehouseAddressCode,
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
	row := s.pool.QueryRow(ctx, autoJobSelect+` WHERE shop_key = $1 AND order_no = $2`, shopKey, orderNo)
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
		filter = " AND status IN ('queued', 'running', 'waiting_confirmation')"
	case "exceptions":
		filter = " AND status = 'failed'"
	case "all", "":
	default:
		return nil, errors.New("unknown SHEIN automatic fulfillment queue")
	}
	rows, err := s.pool.Query(ctx, autoJobSelect+` WHERE shop_key = $1`+filter+` ORDER BY updated_at DESC LIMIT $2`, shopKey, limit)
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

const autoJobSelect = `SELECT id, order_no, status, current_step, attempts,
	shein_sku, warehouse_sku, warehouse_address_code, pre_request_id,
	express_channel_code, COALESCE(performance_cost::text, ''), currency_code,
	place_request_id, delivery_no, error_code, error_message,
	created_at, updated_at, started_at, completed_at
	FROM shein_go_auto_fulfillment_jobs`

type autoJobScanner interface {
	Scan(dest ...any) error
}

func scanAutoJob(scanner autoJobScanner) (AutoFulfillmentJob, error) {
	var job AutoFulfillmentJob
	err := scanner.Scan(&job.ID, &job.OrderNo, &job.Status, &job.CurrentStep, &job.Attempts,
		&job.SheinSKU, &job.WarehouseSKU, &job.WarehouseAddressCode, &job.PreRequestID,
		&job.ExpressChannelCode, &job.PerformanceCost, &job.CurrencyCode,
		&job.PlaceRequestID, &job.DeliveryNo, &job.ErrorCode, &job.ErrorMessage,
		&job.CreatedAt, &job.UpdatedAt, &job.StartedAt, &job.CompletedAt)
	if err != nil {
		return AutoFulfillmentJob{}, fmt.Errorf("scan SHEIN automatic fulfillment job: %w", err)
	}
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
	default:
		return ""
	}
}

func positiveDecimal(value string) bool {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil && parsed > 0
}
