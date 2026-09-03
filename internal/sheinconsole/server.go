package sheinconsole

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"shein-api-manager/internal/shein"
	"shein-api-manager/internal/xlwms"

	"github.com/jackc/pgx/v5"
)

//go:embed web/*
var webFiles embed.FS

const shopHeader = "X-Shein-Shop"

type Server struct {
	store          *shein.Store
	shopKey        string
	shopName       string
	requestTimeout time.Duration
	logger         *slog.Logger
	xlwms          *xlwms.Client
	autoQueue      chan autoQueueRef
	orderSyncMu    sync.Mutex
}

type response struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	Code    string `json:"code,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
	Cached  bool   `json:"cached,omitempty"`
	Meta    any    `json:"meta,omitempty"`
}

type proxyRequest struct {
	ShopKey string         `json:"shop_key"`
	Data    map[string]any `json:"data"`
}

func New(store *shein.Store, shopKey, shopName string, requestTimeout time.Duration, logger *slog.Logger, xlwmsClient *xlwms.Client) http.Handler {
	server := &Server{
		store: store, shopKey: strings.TrimSpace(shopKey), shopName: strings.TrimSpace(shopName),
		requestTimeout: requestTimeout, logger: logger, xlwms: xlwmsClient, autoQueue: make(chan autoQueueRef, 500),
	}
	server.startAutoWorkers()
	server.startInventoryClassification()
	server.startWarehouseWatch()
	return server.routes()
}

func (server *Server) routes() http.Handler {
	mux := http.NewServeMux()
	server.registerRoutes(mux)
	return securityHeaders(mux)
}

func (server *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", server.health)
	mux.Handle("GET /", http.HandlerFunc(server.index))
	mux.Handle("GET /temu/dashboard.css", http.HandlerFunc(server.localDashboardCSS))
	mux.Handle("GET /assets/{name}", http.HandlerFunc(server.asset))
	mux.Handle("GET /api/status", http.HandlerFunc(server.status))
	mux.Handle("GET /api/oms-platform-orders/accounts", http.HandlerFunc(server.xlwmsAccounts))
	mux.Handle("GET /api/oms-platform-orders", http.HandlerFunc(server.listOMSPlatformOrders))
	mux.Handle("POST /api/oms-platform-orders/sync", http.HandlerFunc(server.syncOMSPlatformOrders))
	mux.Handle("GET /api/oms-platform-orders/{orderNo}", http.HandlerFunc(server.xlwmsPlatformOrder))
	mux.Handle("POST /api/order/list", server.operationHandler("order-list"))
	mux.Handle("POST /api/order/detail", server.operationHandler("order-detail"))
	mux.Handle("POST /api/order/export-address", server.operationHandler("export-address"))
	mux.Handle("GET /api/orders", http.HandlerFunc(server.listOrders))
	mux.Handle("GET /api/orders/history", http.HandlerFunc(server.listOrderHistory))
	mux.Handle("GET /api/orders/{orderNo}/detail", http.HandlerFunc(server.getOrderDetail))
	mux.Handle("GET /api/orders/{orderNo}", http.HandlerFunc(server.getOrder))
	mux.Handle("POST /api/orders/sync", http.HandlerFunc(server.syncOrders))
	mux.Handle("POST /api/orders/details/sync", http.HandlerFunc(server.syncOrderDetails))
	mux.Handle("POST /api/label-purchases/lookup", http.HandlerFunc(server.purchasedLabelLookup))
	mux.Handle("GET /api/fulfillment/orders", http.HandlerFunc(server.fulfillmentOrders))
	mux.Handle("POST /api/fulfillment/orders/sync", http.HandlerFunc(server.syncFulfillmentOrders))
	mux.Handle("POST /api/orders/{orderNo}/warehouse-preview", http.HandlerFunc(server.xlwmsWarehousePreview))
	mux.Handle("GET /api/orders/{orderNo}/xlwms-parcel", http.HandlerFunc(server.xlwmsParcelDraft))
	mux.Handle("POST /api/orders/{orderNo}/xlwms-parcel", http.HandlerFunc(server.createXLWMSParcel))
	mux.Handle("POST /api/orders/{orderNo}/xlwms-parcel-label", http.HandlerFunc(server.uploadXLWMSParcelLabel))
	mux.Handle("GET /api/inventory-thresholds", http.HandlerFunc(server.inventoryThresholds))
	mux.Handle("GET /api/inventory-thresholds/defaults", http.HandlerFunc(server.inventoryThresholdDefaults))
	mux.Handle("PATCH /api/inventory-thresholds/defaults", http.HandlerFunc(server.inventoryThresholdDefaults))
	mux.Handle("POST /api/inventory-thresholds/defaults/reset", http.HandlerFunc(inventoryThresholdDefaultResetGone))
	mux.Handle("PATCH /api/inventory-thresholds/{warehouseSKU}", http.HandlerFunc(server.updateSKUInventoryThreshold))
	mux.Handle("POST /api/inventory-thresholds/{warehouseSKU}/reset", http.HandlerFunc(server.resetSKUInventoryThreshold))
	mux.Handle("GET /api/carrier-policies", http.HandlerFunc(fulfillmentPoliciesMoved))
	mux.Handle("PUT /api/carrier-policies/{warehouseKey}", http.HandlerFunc(fulfillmentPoliciesMoved))
	mux.Handle("GET /api/auto-fulfillment/jobs", http.HandlerFunc(server.autoFulfillmentJobs))
	mux.Handle("POST /api/auto-fulfillment/run", http.HandlerFunc(server.runAutoFulfillment))
	mux.Handle("GET /api/auto-fulfillment/batches/latest", http.HandlerFunc(server.latestAutoFulfillmentBatch))
	mux.Handle("GET /api/shipping/tasks", http.HandlerFunc(server.fulfillmentTasks))
	mux.Handle("POST /api/shipping/tasks/{orderNo}/resolve", http.HandlerFunc(server.resolveFulfillmentTask))
	mux.Handle("POST /api/shipping/warehouses", server.operationHandler("available-shipping-warehouse"))
	mux.Handle("POST /api/shipping/channels", server.operationHandler("order-mapping-channels"))
	mux.Handle("POST /api/shipping/place", server.operationHandler("place-express-order"))
	mux.Handle("POST /api/shipping/check", server.operationHandler("check-express-order"))
	mux.Handle("POST /api/shipping/label", server.operationHandler("print-express-info"))
	mux.Handle("GET /api/shipping/track", http.HandlerFunc(server.logisticsTrack))
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]string{
		"status": "ok", "service": "shein-go-manager", "shop_code": s.shopKey, "shop_name": s.shopName,
	}})
}

func (s *Server) index(writer http.ResponseWriter, _ *http.Request) {
	content, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(writer, "SHEIN console is unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(content)
}

// localDashboardCSS keeps the standalone console usable when it is opened
// directly rather than through the shared /temu/ Nginx route.
func (s *Server) localDashboardCSS(writer http.ResponseWriter, _ *http.Request) {
	content, err := webFiles.ReadFile("web/styles.css")
	if err != nil {
		http.Error(writer, "SHEIN console styles are unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/css; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(content)
}

func (s *Server) asset(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	contentType := ""
	switch name {
	case "app.js", "xlwms.js":
		contentType = "text/javascript; charset=utf-8"
	case "styles.css", "platform.css":
		contentType = "text/css; charset=utf-8"
	default:
		http.NotFound(writer, request)
		return
	}
	content, err := webFiles.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(content)
}

func (s *Server) status(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, "")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]any{
		"service": "shein-go-manager", "shop_code": shopKey, "shop_name": s.shopName, "endpoints": len(shein.Endpoints),
	}})
}

func (s *Server) fulfillmentTasks(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	tasks, err := s.store.ListFulfillmentTasks(ctx, shopKey, 200)
	if err != nil {
		s.internalError(writer, "list fulfillment tasks", err)
		return
	}
	if err := s.store.AttachOrderFulfillmentStates(ctx, shopKey, tasks); err != nil {
		s.internalError(writer, "attach SHEIN order fulfillment states", err)
		return
	}
	s.attachLingxingParcelStatusToTasks(ctx, shopKey, tasks)
	writeJSON(writer, http.StatusOK, response{Success: true, Data: tasks})
}

func (s *Server) operationHandler(operation string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload proxyRequest
		if !decodeJSON(writer, request, &payload) {
			return
		}
		if err := shein.Validate(operation, payload.Data); err != nil {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		shopKey, err := s.requestedShopKey(request, payload.ShopKey)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		confirmValue, requiresIdempotency, err := requiredConfirmation(operation, payload.Data)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		if confirmValue != "" && request.Header.Get("X-Confirm-Shein-Action") != confirmValue {
			writeJSON(writer, http.StatusPreconditionRequired, response{Success: false, Error: "explicit action confirmation is required"})
			return
		}
		idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
		requestHash := hashRequest(payload.Data)
		ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
		defer cancel()
		if requiresIdempotency {
			record, reserved, reserveErr := s.store.ReserveOperation(ctx, shopKey, operation, idempotencyKey, requestHash)
			if reserveErr != nil {
				writeJSON(writer, http.StatusConflict, response{Success: false, Error: reserveErr.Error()})
				return
			}
			if !reserved {
				switch record.Status {
				case "completed":
					s.persistFulfillmentState(shopKey, operation, payload.Data, record.Response)
					writeJSON(writer, http.StatusOK, response{Success: true, Data: record.Response, Cached: true})
				case "pending":
					writeJSON(writer, http.StatusConflict, response{Success: false, Error: "the same operation is already in progress"})
				default:
					writeJSON(writer, http.StatusConflict, response{Success: false, Error: "the previous operation failed; review it before using a new idempotency key"})
				}
				return
			}
		}
		credentials, err := s.store.Credentials(ctx, shopKey)
		if err != nil {
			if requiresIdempotency {
				_ = s.store.FailOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, operationErrorSummary(err))
			}
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		if operation == "place-express-order" {
			if err := s.rejectDisabledCarrierPurchase(ctx, shopKey, payload.Data); err != nil {
				_ = s.store.FailOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, operationErrorSummary(err))
				writeJSON(writer, http.StatusConflict, response{Success: false, Error: err.Error()})
				return
			}
			recorded, snapshotErr := s.store.ReserveLabelPurchaseChoice(
				ctx, shopKey, firstString(payload.Data, "preRequestId"),
				firstString(payload.Data, "expressChannelCode"), idempotencyKey,
			)
			if snapshotErr != nil {
				_ = s.store.FailOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, operationErrorSummary(snapshotErr))
				s.logger.Error("reserve SHEIN price snapshot failed", "shop", shopKey, "error", sanitizedError(snapshotErr))
				writeJSON(writer, http.StatusConflict, response{Success: false, Error: "无法保存购单价格快照，请重新查询物流渠道"})
				return
			}
			if !recorded {
				s.logger.Info("SHEIN legacy quote has no price snapshot", "shop", shopKey)
			}
		}
		if operation == "print-express-info" {
			if err := s.rejectUnprintableSHEINLabel(ctx, shopKey, payload.Data); err != nil {
				writeJSON(writer, http.StatusConflict, response{Success: false, Error: err.Error()})
				return
			}
		}
		started := time.Now()
		result, err := shein.NewClient(credentials, s.requestTimeout).Call(ctx, operation, payload.Data)
		if err != nil {
			if requiresIdempotency {
				_ = s.store.FailOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, operationErrorSummary(err))
			}
			s.writeAPIError(writer, err)
			s.logger.Warn("SHEIN operation failed", "operation", operation, "shop", shopKey, "duration_ms", time.Since(started).Milliseconds(), "error", sanitizedError(err))
			return
		}
		if operation == "order-mapping-channels" {
			if err := s.applyCarrierPoliciesToChannelResult(ctx, shopKey, payload.Data, result); err != nil {
				s.internalError(writer, "apply carrier policies", err)
				return
			}
			if err := s.saveShippingQuote(shopKey, payload.Data, result); err != nil {
				s.internalError(writer, "save shipping quote", err)
				return
			}
		}
		if requiresIdempotency {
			storedResult := cacheableOperationResponse(operation, result)
			if err := s.store.CompleteOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, storedResult); err != nil {
				s.internalError(writer, "save operation result", err)
				return
			}
		}
		s.persistFulfillmentState(shopKey, operation, payload.Data, result)
		s.logger.Info("SHEIN operation completed", "operation", operation, "shop", shopKey, "duration_ms", time.Since(started).Milliseconds())
		writeJSON(writer, http.StatusOK, response{Success: true, Data: result})
	})
}

func (s *Server) logisticsTrack(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	credentials, err := s.store.Credentials(ctx, shopKey)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	result, err := shein.NewClient(credentials, s.requestTimeout).LogisticsTrack(
		ctx,
		request.URL.Query().Get("orderNo"),
		request.URL.Query().Get("packageNo"),
		request.URL.Query().Get("waybillNo"),
		request.URL.Query().Get("returnOrderNo"),
	)
	if err != nil {
		s.writeAPIError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: result})
}

func (s *Server) requestedShopKey(request *http.Request, payloadShopKey string) (string, error) {
	configuredShopKey := strings.TrimSpace(s.shopKey)
	if configuredShopKey == "" {
		configuredShopKey = "default"
	}
	headerShopKey := strings.TrimSpace(request.Header.Get(shopHeader))
	payloadShopKey = strings.TrimSpace(payloadShopKey)
	if headerShopKey != "" && headerShopKey != configuredShopKey {
		return "", errors.New("X-Shein-Shop does not match the routed shop")
	}
	if payloadShopKey != "" && payloadShopKey != configuredShopKey {
		return "", errors.New("shop_key does not match the routed shop")
	}
	return configuredShopKey, nil
}

func requiredConfirmation(operation string, data map[string]any) (string, bool, error) {
	switch operation {
	case "place-express-order":
		return "place-express-order", true, nil
	case "print-express-info":
		return "print-express-info", true, nil
	case "export-address":
		handleType, ok := integerValue(data["handleType"])
		if ok && handleType == 2 {
			return "export-address-transition", true, nil
		}
	}
	return "", false, nil
}

func hashRequest(data map[string]any) string {
	encoded, _ := json.Marshal(data)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 2<<20))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "invalid JSON request"})
		return false
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "request body must contain one JSON object"})
		return false
	}
	return true
}

func fulfillmentPoliciesMoved(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusGone, response{Success: false, Error: "发货策略已迁移到 XLWMS 仓库运营中台"})
}

func (s *Server) carrierPolicies(ctx context.Context, warehouseSKU string) ([]shein.WarehouseCarrierPolicies, error) {
	if s.xlwms == nil {
		return nil, errors.New("XLWMS client is unavailable")
	}
	groups, err := s.xlwms.CarrierPolicies(ctx, "shein", warehouseSKU)
	if err != nil {
		return nil, err
	}
	result := make([]shein.WarehouseCarrierPolicies, 0, len(groups))
	for _, group := range groups {
		carriers := make([]shein.CarrierPolicy, 0, len(group.Carriers))
		for _, policy := range group.Carriers {
			carriers = append(carriers, shein.CarrierPolicy{WarehouseKey: policy.WarehouseKey, CarrierCode: policy.CarrierCode, Priority: policy.Priority, Enabled: policy.Enabled})
		}
		rules := shein.WarehouseCarrierRules{
			WarehouseKey: group.BaseRules.WarehouseKey, AllowedCarrierCodes: append([]string(nil), group.BaseRules.AllowedCarrierCodes...),
			AllowSignature: group.BaseRules.AllowSignature, AllowedCurrencyCodes: append([]string(nil), group.BaseRules.AllowedCurrencyCodes...),
			SelectionMode: group.BaseRules.SelectionMode, MaxPriceDelta: group.BaseRules.MaxPriceDelta,
			WarehouseTiePriority: group.BaseRules.WarehouseTiePriority,
		}
		if rules.WarehouseKey == "" {
			return nil, fmt.Errorf("XLWMS did not return base carrier rules for %s", group.WarehouseKey)
		}
		result = append(result, shein.WarehouseCarrierPolicies{WarehouseKey: group.WarehouseKey, BaseRules: rules, Carriers: carriers})
	}
	return result, nil
}

func (s *Server) orderWarehouseSKU(ctx context.Context, shopKey, orderNo string) string {
	goods, err := s.store.MappedOrderGoods(ctx, shopKey, orderNo)
	if err != nil {
		return ""
	}
	unique := ""
	for _, item := range goods {
		sku := strings.TrimSpace(item.WarehouseSKU)
		if sku == "" {
			continue
		}
		if unique != "" && unique != sku {
			return ""
		}
		unique = sku
	}
	return unique
}

func (s *Server) applyCarrierPoliciesToChannelResult(ctx context.Context, shopKey string, requestData, result map[string]any) error {
	warehouseSKU := s.orderWarehouseSKU(ctx, shopKey, firstString(requestData, "orderNo"))
	groups, err := s.carrierPolicies(ctx, warehouseSKU)
	if err != nil {
		return err
	}
	warehouseCode := firstString(requestData, "warehouseAddressCode", "warehouseCode")
	warehouseName := firstString(requestData, "warehouseName", "warehouseAddressName", "warehouseDesc")
	if warehouseCode == "" {
		warehouseCode = firstString(firstObject(result["info"]), "warehouseAddressCode", "warehouseCode")
	}
	if warehouseName == "" {
		warehouseName = firstString(firstObject(result["info"]), "warehouseName", "warehouseAddressName", "warehouseDesc")
	}
	policyGroup := shein.PoliciesByWarehouse(groups)[shein.PolicyWarehouseKey(warehouseCode, warehouseName)]
	shein.ApplyCarrierPoliciesToChannels(result, warehouseCode, warehouseName, policyGroup)
	return nil
}

func (s *Server) rejectDisabledCarrierPurchase(ctx context.Context, shopKey string, data map[string]any) error {
	preRequestID := firstString(data, "preRequestId")
	channelCode := firstString(data, "expressChannelCode")
	if preRequestID == "" || channelCode == "" {
		return nil
	}
	warehouseCode, orderNo, err := s.store.ShippingQuoteWarehouseAndOrder(ctx, shopKey, preRequestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	groups, err := s.carrierPolicies(ctx, s.orderWarehouseSKU(ctx, shopKey, orderNo))
	if err != nil {
		return err
	}
	reason := shein.ChannelUnavailableReason(
		channelCode, "", "", "", warehouseCode, "", false,
		shein.PoliciesByWarehouse(groups)[shein.PolicyWarehouseKey(warehouseCode, "")],
	)
	if reason != "" {
		return errors.New(reason)
	}
	return nil
}

func (s *Server) rejectUnprintableSHEINLabel(ctx context.Context, shopKey string, data map[string]any) error {
	if s.store == nil {
		return nil
	}
	deliveryNo, orderNo, _ := fulfillmentLabelIdentifiers(data)
	task, err := sheinFulfillmentTaskForLabel(ctx, s.store, shopKey, orderNo, deliveryNo)
	if err != nil {
		if errors.Is(err, shein.ErrFulfillmentTaskNotFound) {
			return nil
		}
		return err
	}
	if shein.LabelPrintable(task) {
		return nil
	}
	return errors.New("SHEIN 已揽收或已签收，面单不能再打印")
}

func sheinFulfillmentTaskForLabel(ctx context.Context, store *shein.Store, shopKey, orderNo, deliveryNo string) (shein.FulfillmentTask, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo != "" {
		task, err := store.LatestFulfillmentTask(ctx, shopKey, orderNo)
		if err == nil {
			return labeledTaskWithOrderState(ctx, store, shopKey, task)
		}
		if !errors.Is(err, shein.ErrFulfillmentTaskNotFound) {
			return shein.FulfillmentTask{}, err
		}
	}
	if strings.TrimSpace(deliveryNo) == "" {
		return shein.FulfillmentTask{}, shein.ErrFulfillmentTaskNotFound
	}
	tasks, err := store.ListFulfillmentTasks(ctx, shopKey, 200)
	if err != nil {
		return shein.FulfillmentTask{}, err
	}
	if err := store.AttachOrderFulfillmentStates(ctx, shopKey, tasks); err != nil {
		return shein.FulfillmentTask{}, err
	}
	for _, task := range tasks {
		if strings.TrimSpace(task.DeliveryNo) == strings.TrimSpace(deliveryNo) {
			return task, nil
		}
	}
	return shein.FulfillmentTask{}, shein.ErrFulfillmentTaskNotFound
}

func labeledTaskWithOrderState(ctx context.Context, store *shein.Store, shopKey string, task shein.FulfillmentTask) (shein.FulfillmentTask, error) {
	loaded := []shein.FulfillmentTask{task}
	if err := store.AttachOrderFulfillmentStates(ctx, shopKey, loaded); err != nil {
		return shein.FulfillmentTask{}, err
	}
	return loaded[0], nil
}

func (s *Server) writeAPIError(writer http.ResponseWriter, err error) {
	var apiErr *shein.APIError
	if errors.As(err, &apiErr) {
		writeJSON(writer, http.StatusBadGateway, response{Success: false, Error: apiErr.Message, Code: apiErr.Code, TraceID: apiErr.TraceID})
		return
	}
	writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
}

func (s *Server) internalError(writer http.ResponseWriter, action string, err error) {
	s.logger.Error("SHEIN console internal error", "action", action, "error", sanitizedError(err))
	writeJSON(writer, http.StatusInternalServerError, response{Success: false, Error: "internal service error"})
}

func sanitizedError(err error) string {
	var apiErr *shein.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("code=%s trace_id=%s", apiErr.Code, apiErr.TraceID)
	}
	return err.Error()
}

func operationErrorSummary(err error) string {
	var apiErr *shein.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("code=%s trace_id=%s", apiErr.Code, apiErr.TraceID)
	}
	return "operation failed"
}

func cacheableOperationResponse(operation string, result map[string]any) map[string]any {
	if operation != "export-address" {
		return result
	}
	return map[string]any{
		"code":    result["code"],
		"msg":     result["msg"],
		"traceId": result["traceId"],
		"info":    map[string]any{"redacted": true},
	}
}

func integerValue(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		integer, err := strconv.Atoi(typed.String())
		return integer, err == nil
	case float64:
		integer := int(typed)
		return integer, float64(integer) == typed
	case int:
		return typed, true
	default:
		return 0, false
	}
}

func queryInt(request *http.Request, name string, fallback int) int {
	value, err := strconv.Atoi(request.URL.Query().Get(name))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func writeJSON(writer http.ResponseWriter, status int, payload response) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "SAMEORIGIN")
		writer.Header().Set("Referrer-Policy", "same-origin")
		writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data: https:; connect-src 'self'; frame-ancestors 'self'")
		next.ServeHTTP(writer, request)
	})
}
