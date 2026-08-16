package sheinconsole

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"shein-api-manager/internal/shein"

	"github.com/jackc/pgx/v5"
)

const (
	autoWorkerCount     = 3
	autoMaxAttempts     = 12
	autoRetryDelay      = 30 * time.Second
	autoOperationWindow = 3 * time.Minute
)

type autoQueueRef struct {
	ShopKey string
	OrderNo string
}

type autoRunRequest struct {
	OrderNos []string `json:"order_nos"`
	Confirm  bool     `json:"confirm"`
}

type quotedChannel struct {
	Quote     shein.ShippingQuote
	Candidate shein.ShippingQuoteCandidate
}

func (s *Server) startAutoWorkers() {
	for index := 0; index < autoWorkerCount; index++ {
		go func() {
			for ref := range s.autoQueue {
				s.processAutoFulfillment(ref)
			}
		}()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		jobs, err := s.store.ListResumableAutoJobs(ctx, s.shopKey)
		if err != nil {
			s.logger.Error("recover SHEIN automatic fulfillment jobs failed", "error", sanitizedError(err))
			return
		}
		for _, job := range jobs {
			s.enqueueAutoJob(autoQueueRef{ShopKey: job[0], OrderNo: job[1]})
		}
		if _, err := s.enqueueNextBulkItem(ctx, s.shopKey); err != nil {
			s.logger.Error("recover SHEIN automatic batch item failed", "shop", s.shopKey, "error", sanitizedError(err))
		}
	}()
}

func (s *Server) fulfillmentOrders(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	queue := strings.TrimSpace(request.URL.Query().Get("queue"))
	if queue == "" {
		queue = "pending"
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	orders, err := s.store.ListOrderQueue(ctx, shopKey, queue)
	if err != nil {
		if strings.Contains(err.Error(), "unknown SHEIN fulfillment queue") {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		s.internalError(writer, "list fulfillment order queue", err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: orders})
}

func (s *Server) syncFulfillmentOrders(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	synced, err := s.syncOpenOrders(ctx, shopKey)
	if err != nil {
		s.writeAPIError(writer, err)
		return
	}
	pending, err := s.store.ListOrderQueue(ctx, shopKey, "pending")
	if err != nil {
		s.internalError(writer, "list synced fulfillment orders", err)
		return
	}
	manual, err := s.store.ListOrderQueue(ctx, shopKey, "manual")
	if err != nil {
		s.internalError(writer, "list synced manual orders", err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]any{
		"synced": synced, "pending": pending, "manual_count": len(manual),
	}})
}

func (s *Server) autoFulfillmentJobs(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	queue := strings.TrimSpace(request.URL.Query().Get("queue"))
	if queue == "" {
		queue = "all"
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	jobs, err := s.store.ListAutoJobs(ctx, shopKey, queue, 500)
	if err != nil {
		if strings.Contains(err.Error(), "unknown SHEIN automatic fulfillment queue") {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		s.internalError(writer, "list automatic fulfillment jobs", err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: jobs})
}

func (s *Server) latestAutoFulfillmentBatch(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	batch, err := s.store.LatestBulkFulfillmentBatch(ctx, shopKey)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]any{}})
		return
	}
	if err != nil {
		s.internalError(writer, "load latest automatic fulfillment batch", err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: batch})
}

func (s *Server) runAutoFulfillment(writer http.ResponseWriter, request *http.Request) {
	var payload autoRunRequest
	if !decodeJSON(writer, request, &payload) {
		return
	}
	if !payload.Confirm || request.Header.Get("X-Confirm-Shein-Action") != "auto-fulfillment" {
		writeJSON(writer, http.StatusPreconditionRequired, response{Success: false, Error: "explicit automatic fulfillment confirmation is required"})
		return
	}
	if len(payload.OrderNos) < 1 || len(payload.OrderNos) > 200 {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "order_nos must contain between 1 and 200 values"})
		return
	}
	shopKey, err := s.requestedShopKey(request, "")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	eligibleOrders, err := s.store.ListOrderQueue(ctx, shopKey, "all")
	if err != nil {
		s.internalError(writer, "validate automatic fulfillment orders", err)
		return
	}
	eligible := make(map[string]bool, len(eligibleOrders))
	for _, order := range eligibleOrders {
		eligible[order.OrderNo] = order.AutoEligible &&
			(order.Job == nil || order.Job.Status == "failed")
	}
	rejected := make(map[string]string)
	seen := make(map[string]bool)
	accepted := make([]string, 0, len(payload.OrderNos))
	for _, rawOrderNo := range payload.OrderNos {
		orderNo := strings.TrimSpace(rawOrderNo)
		if orderNo == "" || seen[orderNo] {
			continue
		}
		seen[orderNo] = true
		if !eligible[orderNo] {
			rejected[orderNo] = "订单不在当前可自动履约队列"
			continue
		}
		accepted = append(accepted, orderNo)
	}
	if len(accepted) == 0 {
		writeJSON(writer, http.StatusConflict, response{Success: false, Error: "没有订单进入自动履约队列", Data: map[string]any{"rejected": rejected}})
		return
	}
	queued := make([]shein.AutoFulfillmentJob, 0, 1)
	var batch *shein.BulkFulfillmentBatch
	if len(accepted) > 1 {
		batchID := "shein-bulk-" + hashRequest(map[string]any{
			"shop": shopKey, "orders": accepted, "created": time.Now().UnixNano(),
		})[:24]
		createdBatch, created, err := s.store.CreateBulkFulfillmentBatch(ctx, shopKey, batchID, accepted)
		if err != nil {
			s.internalError(writer, "create automatic fulfillment batch", err)
			return
		}
		batch = &createdBatch
		if !created {
			writeJSON(writer, http.StatusConflict, response{
				Success: false, Error: "已有一键发货批次正在运行", Data: map[string]any{"batch": createdBatch},
			})
			return
		}
		job, err := s.enqueueNextBulkItem(ctx, shopKey)
		if err != nil {
			s.internalError(writer, "start automatic fulfillment batch", err)
			return
		}
		if job != nil {
			queued = append(queued, *job)
		}
	} else {
		orderNo := accepted[0]
		if _, err := s.store.RestartStoppedBulkItem(ctx, shopKey, orderNo); err != nil {
			s.internalError(writer, "restart automatic fulfillment batch", err)
			return
		}
		job, created, err := s.store.EnqueueAutoJob(ctx, shopKey, orderNo)
		if err != nil {
			s.internalError(writer, "enqueue automatic fulfillment order", err)
			return
		}
		if !created {
			writeJSON(writer, http.StatusConflict, response{Success: false, Error: "订单已有自动履约任务"})
			return
		}
		queued = append(queued, job)
		s.enqueueAutoJob(autoQueueRef{ShopKey: shopKey, OrderNo: orderNo})
	}
	writeJSON(writer, http.StatusAccepted, response{Success: true, Data: map[string]any{
		"queued": queued, "rejected": rejected, "batch": batch,
	}})
}

func (s *Server) enqueueNextBulkItem(ctx context.Context, shopKey string) (*shein.AutoFulfillmentJob, error) {
	item, _, err := s.store.StartNextBulkFulfillmentItem(ctx, shopKey)
	if err != nil {
		return nil, err
	}
	if item.OrderNo == "" {
		return nil, nil
	}
	job, created, err := s.store.EnqueueAutoJob(ctx, shopKey, item.OrderNo)
	if err != nil {
		return nil, err
	}
	if !created && job.Status == "completed" {
		batch, found, err := s.store.FinishBulkFulfillmentItem(ctx, shopKey, item.OrderNo, "succeeded", "")
		if err != nil {
			return nil, err
		}
		if found && batch.Status == "running" {
			return s.enqueueNextBulkItem(ctx, shopKey)
		}
		return nil, nil
	}
	if created || job.Status == "queued" {
		s.enqueueAutoJob(autoQueueRef{ShopKey: shopKey, OrderNo: item.OrderNo})
	}
	return &job, nil
}

func (s *Server) enqueueAutoJob(ref autoQueueRef) {
	select {
	case s.autoQueue <- ref:
	default:
		s.logger.Error("SHEIN automatic fulfillment queue is full", "shop", ref.ShopKey, "order", ref.OrderNo)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = s.store.SetAutoJobState(ctx, ref.ShopKey, ref.OrderNo, "failed", "queue", "queue_full", "自动履约队列已满")
		s.finishBulkItem(ctx, ref, "failed", "自动履约队列已满")
		cancel()
	}
}

func (s *Server) syncOpenOrders(ctx context.Context, shopKey string) (int, error) {
	credentials, err := s.store.Credentials(ctx, shopKey)
	if err != nil {
		return 0, err
	}
	client := shein.NewClient(credentials, s.requestTimeout)
	location := time.FixedZone("UTC+8", 8*60*60)
	endTime := time.Now().In(location)
	startTime := endTime.Add(-47 * time.Hour)
	listByOrder := make(map[string]map[string]any)
	orderNumbers := make(map[string]bool)

	for _, status := range []int{1, 2} {
		for page := 1; page <= 20; page++ {
			result, err := client.Call(ctx, "order-list", map[string]any{
				"queryType": 2, "startTime": startTime.Format(shein.SHEINTimeFormat),
				"endTime": endTime.Format(shein.SHEINTimeFormat), "orderStatus": status,
				"page": page, "pageSize": shein.MaxOrderListPageSize,
			})
			if err != nil {
				return 0, err
			}
			orders := collectOrderObjects(result["info"], false)
			for _, order := range orders {
				orderNo := orderNumberFromMap(order)
				if orderNo == "" {
					continue
				}
				listByOrder[orderNo] = order
				orderNumbers[orderNo] = true
			}
			if len(orders) < shein.MaxOrderListPageSize {
				break
			}
		}
	}
	existing, err := s.store.ListPendingOrderNos(ctx, shopKey)
	if err != nil {
		return 0, err
	}
	for _, orderNo := range existing {
		orderNumbers[orderNo] = true
	}
	numbers := make([]string, 0, len(orderNumbers))
	for orderNo := range orderNumbers {
		numbers = append(numbers, orderNo)
	}
	sort.Strings(numbers)

	snapshots := make([]shein.OrderSnapshot, 0, len(numbers))
	for index := 0; index < len(numbers); index += shein.MaxOrderDetailBatch {
		end := index + shein.MaxOrderDetailBatch
		if end > len(numbers) {
			end = len(numbers)
		}
		batch := numbers[index:end]
		values := make([]any, 0, len(batch))
		for _, orderNo := range batch {
			values = append(values, orderNo)
		}
		result, err := client.Call(ctx, "order-detail", map[string]any{"orderNoList": values})
		if err != nil {
			return len(snapshots), err
		}
		for _, detail := range collectOrderObjects(result["info"], true) {
			orderNo := orderNumberFromMap(detail)
			if orderNo == "" {
				continue
			}
			snapshot := shein.OrderSnapshot{
				OrderNo: orderNo, Status: orderStatusFromMap(detail),
				ListData: listByOrder[orderNo], DetailData: detail,
			}
			snapshots = append(snapshots, snapshot)
		}
		for _, task := range fulfillmentTasksFromOrderDetail(result) {
			if err := s.store.UpsertFulfillmentTask(ctx, shopKey, task); err != nil {
				return len(snapshots), err
			}
		}
	}
	if err := s.store.UpsertOrderSnapshots(ctx, shopKey, snapshots); err != nil {
		return len(snapshots), err
	}
	return len(snapshots), nil
}

func collectOrderObjects(value any, requireDetail bool) []map[string]any {
	objects := make([]map[string]any, 0)
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			orderNo := orderNumberFromMap(typed)
			_, hasGoods := typed["orderGoodsInfoList"]
			if orderNo != "" && (!requireDetail || hasGoods) {
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

func orderNumberFromMap(order map[string]any) string {
	return scalarString(order, "orderNo", "billNo", "orderSn", "orderId", "parentOrderNo")
}

func orderStatusFromMap(order map[string]any) string {
	return scalarString(order, "orderStatus", "newGoodsStatus", "status")
}

func scalarString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := object[key].(type) {
		case string:
			if text := strings.TrimSpace(value); text != "" {
				return text
			}
		case json.Number:
			return value.String()
		case float64:
			return strconv.FormatFloat(value, 'f', -1, 64)
		case int:
			return strconv.Itoa(value)
		}
	}
	return ""
}

func (s *Server) processAutoFulfillment(ref autoQueueRef) {
	claimCtx, claimCancel := context.WithTimeout(context.Background(), 5*time.Second)
	claimed, err := s.store.ClaimAutoJob(claimCtx, ref.ShopKey, ref.OrderNo)
	claimCancel()
	if err != nil || !claimed {
		if err != nil {
			s.logger.Warn("claim SHEIN automatic fulfillment failed", "shop", ref.ShopKey, "order", ref.OrderNo, "error", sanitizedError(err))
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), autoOperationWindow)
	defer cancel()
	if err := s.executeAutoFulfillment(ctx, ref); err != nil {
		code, message := automaticError(err)
		if stateErr := s.store.SetAutoJobState(context.WithoutCancel(ctx), ref.ShopKey, ref.OrderNo, "failed", "failed", code, message); stateErr != nil {
			s.logger.Error("save SHEIN automatic fulfillment failure failed", "shop", ref.ShopKey, "order", ref.OrderNo, "error", sanitizedError(stateErr))
		}
		s.finishBulkItem(context.WithoutCancel(ctx), ref, "failed", message)
		s.logger.Warn("SHEIN automatic fulfillment failed", "shop", ref.ShopKey, "order", ref.OrderNo, "code", code, "error", sanitizedError(err))
		return
	}
	job, err := s.store.GetAutoJob(context.WithoutCancel(ctx), ref.ShopKey, ref.OrderNo)
	if err != nil {
		s.logger.Warn("load completed SHEIN automatic fulfillment failed", "shop", ref.ShopKey, "order", ref.OrderNo, "error", sanitizedError(err))
		return
	}
	if job.Status == "completed" {
		s.finishBulkItem(context.WithoutCancel(ctx), ref, "succeeded", "")
	}
}

func (s *Server) finishBulkItem(ctx context.Context, ref autoQueueRef, status, message string) {
	batch, found, err := s.store.FinishBulkFulfillmentItem(ctx, ref.ShopKey, ref.OrderNo, status, message)
	if err != nil {
		s.logger.Error("finish SHEIN automatic batch item failed", "shop", ref.ShopKey, "order", ref.OrderNo, "error", sanitizedError(err))
		return
	}
	if !found || batch.Status != "running" {
		return
	}
	if _, err := s.enqueueNextBulkItem(ctx, ref.ShopKey); err != nil {
		s.logger.Error("advance SHEIN automatic batch failed", "shop", ref.ShopKey, "error", sanitizedError(err))
	}
}

func (s *Server) executeAutoFulfillment(ctx context.Context, ref autoQueueRef) error {
	job, err := s.store.GetAutoJob(ctx, ref.ShopKey, ref.OrderNo)
	if err != nil {
		return err
	}
	credentials, err := s.store.Credentials(ctx, ref.ShopKey)
	if err != nil {
		return err
	}
	client := shein.NewClient(credentials, s.requestTimeout)
	if job.PlaceRequestID != "" || job.DeliveryNo != "" {
		return s.finishAutomaticOrder(ctx, client, ref, job)
	}

	if err := s.refreshSingleOrder(ctx, client, ref.ShopKey, ref.OrderNo); err != nil {
		return err
	}
	items, err := s.store.ListOrderQueue(ctx, ref.ShopKey, "all")
	if err != nil {
		return err
	}
	var order *shein.OrderQueueItem
	for index := range items {
		if items[index].OrderNo == ref.OrderNo {
			order = &items[index]
			break
		}
	}
	if order == nil {
		return errors.New("订单已不在待履约队列")
	}
	if !order.AutoEligible || order.PackageSpec == nil {
		return errors.New("订单已转入人工处理队列")
	}
	if !shein.CanPurchasePlatformLabel(order.Detail) {
		return errors.New("订单不支持 SHEIN 平台面单购买")
	}
	if shein.RequiresAddressTransition(order.Detail, order.OrderStatus) {
		return errors.New("商家自发货订单不能走平台面单购买")
	}

	if err := s.setAutomaticStep(ctx, ref, "query_warehouses"); err != nil {
		return err
	}
	warehouseResult, err := client.Call(ctx, "available-shipping-warehouse", map[string]any{"orderNo": ref.OrderNo})
	if err != nil {
		return err
	}
	warehouses := availableWarehouses(warehouseResult)
	if len(warehouses) == 0 {
		return errors.New("订单没有可用发货仓")
	}
	if err := s.setAutomaticStep(ctx, ref, "quote_channels"); err != nil {
		return err
	}
	quotes := make([]quotedChannel, 0)
	for _, warehouse := range warehouses {
		warehouseCode := scalarString(warehouse, "warehouseAddressCode", "warehouseCode")
		data, err := channelRequest(ref.OrderNo, warehouseCode, *order.PackageSpec, order.Goods)
		if err != nil {
			return err
		}
		result, err := client.Call(ctx, "order-mapping-channels", data)
		if err != nil {
			return err
		}
		quote, ok := shippingQuoteFromChannels(data, result)
		if !ok {
			return errors.New("物流渠道响应缺少有效报价")
		}
		if err := s.store.SaveShippingQuote(ctx, ref.ShopKey, quote); err != nil {
			return err
		}
		for _, candidate := range quote.Candidates {
			if candidate.PerformanceCost != "" {
				quotes = append(quotes, quotedChannel{Quote: quote, Candidate: candidate})
			}
		}
	}
	selected, err := lowestQuotedChannel(quotes)
	if err != nil {
		return err
	}
	if err := s.store.SetAutoJobSelection(ctx, ref.ShopKey, ref.OrderNo, order.SheinSKU, order.WarehouseSKU,
		selected.Quote.WarehouseAddressCode, selected.Quote.PreRequestID,
		selected.Candidate.ExpressChannelCode, selected.Candidate.PerformanceCost, selected.Candidate.CurrencyCode); err != nil {
		return err
	}
	if err := s.setAutomaticStep(ctx, ref, "place_order"); err != nil {
		return err
	}
	placeData := map[string]any{
		"expressChannelCode": selected.Candidate.ExpressChannelCode,
		"preRequestId":       selected.Quote.PreRequestID,
		"packageInfoList": []any{map[string]any{
			"orderNo": ref.OrderNo, "goodsIds": goodsIDsFromQueue(order.Goods),
		}},
	}
	placeKey := "shein-auto-place-" + hashRequest(placeData)[:32]
	recorded, err := s.store.ReserveLabelPurchaseSelection(
		ctx, ref.ShopKey, selected.Quote.PreRequestID, selected.Candidate.ExpressChannelCode,
		placeKey, "automatic", "lowest_available_price",
	)
	if err != nil {
		return err
	}
	if !recorded {
		return errors.New("无法保存自动购单价格快照")
	}
	placeResult, err := s.callAutomaticOperation(ctx, client, ref.ShopKey, "place-express-order", placeData, placeKey)
	if err != nil {
		return err
	}
	s.persistFulfillmentState(ref.ShopKey, "place-express-order", placeData, placeResult)
	info := firstObject(placeResult["info"])
	placeRequestID := firstString(info, "placeRequestId")
	deliveryNo := firstString(info, "deliveryNo")
	if placeRequestID == "" && deliveryNo == "" {
		return errors.New("在线下单响应缺少履约编号")
	}
	if err := s.store.SetAutoJobResult(ctx, ref.ShopKey, ref.OrderNo, placeRequestID, deliveryNo); err != nil {
		return err
	}
	if err := s.store.UpdateLabelPurchaseResult(ctx, ref.ShopKey, selected.Quote.PreRequestID, placeRequestID, deliveryNo); err != nil {
		return err
	}
	job.PlaceRequestID, job.DeliveryNo = placeRequestID, deliveryNo
	return s.finishAutomaticOrder(ctx, client, ref, job)
}

func (s *Server) refreshSingleOrder(ctx context.Context, client *shein.Client, shopKey, orderNo string) error {
	result, err := client.Call(ctx, "order-detail", map[string]any{"orderNoList": []any{orderNo}})
	if err != nil {
		return err
	}
	details := collectOrderObjects(result["info"], true)
	if len(details) == 0 {
		return errors.New("订单详情接口未返回目标订单")
	}
	found := false
	for _, detail := range details {
		if orderNumberFromMap(detail) != orderNo {
			continue
		}
		found = true
		if err := s.store.UpsertOrderSnapshots(ctx, shopKey, []shein.OrderSnapshot{{
			OrderNo: orderNo, Status: orderStatusFromMap(detail), DetailData: detail,
		}}); err != nil {
			return err
		}
	}
	if !found {
		return errors.New("订单详情接口未返回目标订单")
	}
	for _, task := range fulfillmentTasksFromOrderDetail(result) {
		if err := s.store.UpsertFulfillmentTask(ctx, shopKey, task); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) finishAutomaticOrder(ctx context.Context, client *shein.Client, ref autoQueueRef, job shein.AutoFulfillmentJob) error {
	if err := s.setAutomaticStep(ctx, ref, "check_order"); err != nil {
		return err
	}
	var check fulfillmentCheckResult
	for attempt := 0; attempt < 4; attempt++ {
		data := map[string]any{}
		if job.PlaceRequestID != "" {
			data["placeRequestId"] = job.PlaceRequestID
		} else {
			data["deliveryNo"] = job.DeliveryNo
		}
		result, err := client.Call(ctx, "check-express-order", data)
		if err != nil {
			return err
		}
		check = fulfillmentResultFromCheck(data, result)
		s.persistFulfillmentState(ref.ShopKey, "check-express-order", data, result)
		if check.PlaceRequestID != "" || check.DeliveryNo != "" {
			job.PlaceRequestID = firstNonEmpty(check.PlaceRequestID, job.PlaceRequestID)
			job.DeliveryNo = firstNonEmpty(check.DeliveryNo, job.DeliveryNo)
			if err := s.store.SetAutoJobResult(ctx, ref.ShopKey, ref.OrderNo, job.PlaceRequestID, job.DeliveryNo); err != nil {
				return err
			}
		}
		if check.Status == "failed" {
			return errors.New("SHEIN 拒绝物流下单")
		}
		if check.Status == "ready" || check.Status == "label_ready" {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	if check.Status != "ready" && check.Status != "label_ready" {
		current, err := s.store.GetAutoJob(ctx, ref.ShopKey, ref.OrderNo)
		if err != nil {
			return err
		}
		if current.Attempts >= autoMaxAttempts {
			return errors.New("承运商确认超时，已停止自动重试")
		}
		if err := s.store.SetAutoJobState(ctx, ref.ShopKey, ref.OrderNo, "waiting_confirmation", "check_order", "", "等待承运商确认"); err != nil {
			return err
		}
		time.AfterFunc(autoRetryDelay, func() {
			requeueCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			requeued, err := s.store.RequeueWaitingAutoJob(requeueCtx, ref.ShopKey, ref.OrderNo, autoMaxAttempts)
			if err != nil {
				s.logger.Warn("requeue SHEIN automatic fulfillment failed", "shop", ref.ShopKey, "order", ref.OrderNo, "error", sanitizedError(err))
				return
			}
			if requeued {
				s.enqueueAutoJob(ref)
			}
		})
		return nil
	}
	if check.Status == "label_ready" {
		return s.store.SetAutoJobState(ctx, ref.ShopKey, ref.OrderNo, "completed", "completed", "", "")
	}
	if job.DeliveryNo == "" {
		return errors.New("下单成功但未返回可打印的 deliveryNo")
	}
	if err := s.setAutomaticStep(ctx, ref, "print_label"); err != nil {
		return err
	}
	labelData := map[string]any{"deliveryNo": job.DeliveryNo}
	labelKey := "shein-auto-label-" + hashRequest(labelData)[:24] + "-" + strconv.Itoa(job.Attempts)
	labelResult, err := s.callAutomaticOperation(ctx, client, ref.ShopKey, "print-express-info", labelData, labelKey)
	if err != nil {
		return err
	}
	s.persistFulfillmentState(ref.ShopKey, "print-express-info", labelData, labelResult)
	return s.store.SetAutoJobState(ctx, ref.ShopKey, ref.OrderNo, "completed", "completed", "", "")
}

func (s *Server) setAutomaticStep(ctx context.Context, ref autoQueueRef, step string) error {
	return s.store.SetAutoJobState(ctx, ref.ShopKey, ref.OrderNo, "running", step, "", "")
}

func (s *Server) callAutomaticOperation(ctx context.Context, client *shein.Client, shopKey, operation string,
	data map[string]any, idempotencyKey string) (map[string]any, error) {
	requestHash := hashRequest(data)
	record, reserved, err := s.store.ReserveOperation(ctx, shopKey, operation, idempotencyKey, requestHash)
	if err != nil {
		return nil, err
	}
	if !reserved {
		switch record.Status {
		case "completed":
			return record.Response, nil
		case "pending":
			return nil, errors.New("相同自动履约操作仍在处理中")
		default:
			return nil, errors.New("此前的自动履约操作失败")
		}
	}
	result, err := client.Call(ctx, operation, data)
	if err != nil {
		_ = s.store.FailOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, operationErrorSummary(err))
		return nil, err
	}
	if err := s.store.CompleteOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, cacheableOperationResponse(operation, result)); err != nil {
		return nil, err
	}
	return result, nil
}

func availableWarehouses(result map[string]any) []map[string]any {
	warehouses := objectsWithField(result["info"], "warehouseAddressCode")
	filtered := warehouses[:0]
	for _, warehouse := range warehouses {
		status := scalarString(warehouse, "availableStatus")
		code := scalarString(warehouse, "warehouseAddressCode", "warehouseCode")
		name := scalarString(warehouse, "warehouseName", "warehouseAddressName", "warehouseDesc")
		if (status == "" || status == "1") && shein.IsAllowedShippingWarehouse(code, name) {
			filtered = append(filtered, warehouse)
		}
	}
	sort.SliceStable(filtered, func(left, right int) bool {
		return scalarString(filtered[left], "warehouseAddressCode") < scalarString(filtered[right], "warehouseAddressCode")
	})
	return filtered
}

func objectsWithField(value any, field string) []map[string]any {
	objects := make([]map[string]any, 0)
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			if scalarString(typed, field) != "" {
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

func channelRequest(orderNo, warehouseCode string, spec shein.PackageSpec, goods []shein.QueueGoods) (map[string]any, error) {
	weightKG, err := strconv.ParseFloat(spec.WeightKG, 64)
	if err != nil || weightKG <= 0 {
		return nil, errors.New("仓库商品重量无效")
	}
	data := map[string]any{
		"orderNo": orderNo, "warehouseAddressCode": warehouseCode,
		"packageSizeInfo": map[string]any{
			"packageLength": spec.LengthCM, "packageWidth": spec.WidthCM,
			"packageHeight": spec.HeightCM, "unit": "cm",
		},
		"packageWeightInfo": map[string]any{
			"packageWeight": strconv.FormatFloat(weightKG*1000, 'f', -1, 64), "unit": "g",
		},
	}
	ids := goodsIDsFromQueue(goods)
	if len(ids) > 0 {
		data["prePackageInfo"] = map[string]any{"goodsIds": ids}
	}
	return data, nil
}

func goodsIDsFromQueue(goods []shein.QueueGoods) []any {
	values := make([]any, 0, len(goods))
	for _, goodsItem := range goods {
		if goodsItem.GoodsID != "" {
			values = append(values, goodsItem.GoodsID)
		}
	}
	return values
}

func lowestQuotedChannel(quotes []quotedChannel) (quotedChannel, error) {
	if len(quotes) == 0 {
		return quotedChannel{}, errors.New("没有可用物流渠道报价")
	}
	currency := quotes[0].Candidate.CurrencyCode
	for _, quote := range quotes {
		if quote.Candidate.CurrencyCode != currency {
			return quotedChannel{}, errors.New("物流报价币种不一致，需人工选择")
		}
	}
	sort.SliceStable(quotes, func(left, right int) bool {
		leftCost, leftErr := strconv.ParseFloat(quotes[left].Candidate.PerformanceCost, 64)
		rightCost, rightErr := strconv.ParseFloat(quotes[right].Candidate.PerformanceCost, 64)
		if leftErr != nil {
			return false
		}
		if rightErr != nil {
			return true
		}
		if leftCost != rightCost {
			return leftCost < rightCost
		}
		if quotes[left].Quote.WarehouseAddressCode != quotes[right].Quote.WarehouseAddressCode {
			return quotes[left].Quote.WarehouseAddressCode < quotes[right].Quote.WarehouseAddressCode
		}
		return quotes[left].Candidate.ExpressChannelCode < quotes[right].Candidate.ExpressChannelCode
	})
	if _, err := strconv.ParseFloat(quotes[0].Candidate.PerformanceCost, 64); err != nil {
		return quotedChannel{}, errors.New("物流报价缺少可比较价格")
	}
	return quotes[0], nil
}

func automaticError(err error) (string, string) {
	var apiErr *shein.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.Code
		if code == "" {
			code = "api_error"
		}
		message := "SHEIN API 调用失败"
		if apiErr.TraceID != "" {
			message += "，Trace ID: " + apiErr.TraceID
		}
		return code, message
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "自动履约执行超时"
	}
	return "workflow_error", err.Error()
}
