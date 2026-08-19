package sheinconsole

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
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

type xlwmsParcelDraftProduct struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

const lingxingPlatformLabelChannel = "Upload_Shipping_Label"

type xlwmsParcelDraft struct {
	Required           bool                      `json:"required"`
	Reason             string                    `json:"reason,omitempty"`
	Warehouse          string                    `json:"warehouse,omitempty"`
	WarehouseHint      string                    `json:"warehouse_hint,omitempty"`
	OrderNo            string                    `json:"order_no,omitempty"`
	SalesPlatform      string                    `json:"sales_platform,omitempty"`
	StoreName          string                    `json:"store_name,omitempty"`
	ChannelCode        string                    `json:"channel_code,omitempty"`
	ChannelHint        string                    `json:"channel_hint,omitempty"`
	TrackingNumber     string                    `json:"tracking_number,omitempty"`
	Receiver           string                    `json:"receiver"`
	Phone              string                    `json:"phone"`
	CountryRegionCode  string                    `json:"country_region_code"`
	ProvinceCode       string                    `json:"province_code"`
	ProvinceName       string                    `json:"province_name"`
	CityName           string                    `json:"city_name"`
	PostCode           string                    `json:"post_code"`
	AddressOne         string                    `json:"address_one"`
	AddressTwo         string                    `json:"address_two,omitempty"`
	Products           []xlwmsParcelDraftProduct `json:"products"`
	MissingFields      []string                  `json:"missing_fields"`
	Ready              bool                      `json:"ready"`
	OutboundOrderNo    string                    `json:"outbound_order_no,omitempty"`
	OutboundStatus     *int                      `json:"outbound_status,omitempty"`
	OutboundStatusName string                    `json:"outbound_status_name,omitempty"`
	LabelAttached      bool                      `json:"label_attached"`
	CanUploadLabel     bool                      `json:"can_upload_label"`
	UploadHint         string                    `json:"upload_hint,omitempty"`
}

type xlwmsParcelCreateRequest struct {
	Warehouse         string                    `json:"warehouse"`
	PlatformOrderNo   string                    `json:"platform_order_no"`
	ThirdOrderNo      string                    `json:"third_order_no"`
	SalesPlatform     string                    `json:"sales_platform"`
	StoreName         string                    `json:"store_name"`
	ChannelCode       string                    `json:"channel_code"`
	TrackingNumber    string                    `json:"tracking_number"`
	Receiver          string                    `json:"receiver"`
	Phone             string                    `json:"phone"`
	CountryRegionCode string                    `json:"country_region_code"`
	ProvinceCode      string                    `json:"province_code"`
	ProvinceName      string                    `json:"province_name"`
	CityName          string                    `json:"city_name"`
	PostCode          string                    `json:"post_code"`
	AddressOne        string                    `json:"address_one"`
	AddressTwo        string                    `json:"address_two"`
	Products          []xlwmsParcelDraftProduct `json:"products"`
}

type xlwmsParcelLabelUploadRequest struct {
	Warehouse      string `json:"warehouse"`
	TrackingNumber string `json:"tracking_number"`
}

func (s *Server) xlwmsParcelDraft(writer http.ResponseWriter, request *http.Request) {
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
	task, err := s.store.LatestFulfillmentTask(ctx, shopKey, orderNo)
	if err != nil {
		if errors.Is(err, shein.ErrFulfillmentTaskNotFound) {
			writeJSON(writer, http.StatusNotFound, response{Success: false, Error: "当前店铺中未找到该履约任务"})
			return
		}
		s.internalError(writer, "load fulfillment task for XLWMS parcel draft", err)
		return
	}
	loaded := []shein.FulfillmentTask{task}
	if err := s.store.AttachOrderFulfillmentStates(ctx, shopKey, loaded); err != nil {
		s.internalError(writer, "attach SHEIN order state for XLWMS parcel draft", err)
		return
	}
	task = loaded[0]
	draft := parcelDraftFromTask(shopKey, s.shopName, task)
	if !draft.Required {
		writeJSON(writer, http.StatusOK, response{Success: true, Data: draft})
		return
	}
	detail, err := s.store.OrderDetail(ctx, shopKey, orderNo)
	if err != nil && !errors.Is(err, shein.ErrOrderNotFound) {
		s.internalError(writer, "load order for XLWMS parcel draft", err)
		return
	}
	applyOrderDetailToParcelDraft(&draft, detail)
	if address, err := s.exportAddressForParcel(ctx, shopKey, orderNo); err != nil {
		draft.MissingFields = appendUnique(draft.MissingFields, "收件地址")
	} else {
		applyAddressToParcelDraft(&draft, address)
	}
	applyMappedProductsToParcelDraft(ctx, s.store, shopKey, &draft, orderNo)
	applyLingxingParcelStatusToDraft(ctx, s.xlwms, &draft)
	finalizeParcelDraft(&draft)
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, response{Success: true, Data: draft})
}

func (s *Server) createXLWMSParcel(writer http.ResponseWriter, request *http.Request) {
	if s.xlwms == nil {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "领星查询服务未配置"})
		return
	}
	orderNo := strings.TrimSpace(request.PathValue("orderNo"))
	if orderNo == "" {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "平台订单号不能为空"})
		return
	}
	var payload xlwmsParcelCreateRequest
	if !decodeJSON(writer, request, &payload) {
		return
	}
	shopKey, err := s.requestedShopKey(request, payloadShopKeyFromRequest(request))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	task, err := s.store.LatestFulfillmentTask(ctx, shopKey, orderNo)
	if err != nil {
		if errors.Is(err, shein.ErrFulfillmentTaskNotFound) {
			writeJSON(writer, http.StatusNotFound, response{Success: false, Error: "当前店铺中未找到该履约任务"})
			return
		}
		s.internalError(writer, "load fulfillment task for XLWMS parcel create", err)
		return
	}
	if !shein.RequiresManualParcelCreate(shopKey, task.WarehouseAddressCode, "") {
		writeJSON(writer, http.StatusConflict, response{Success: false, Error: "当前发货仓不需要 DPS 出库单"})
		return
	}
	if task.Status != "label_ready" && task.Status != "ready" {
		writeJSON(writer, http.StatusConflict, response{Success: false, Error: "当前订单尚未到面单阶段，不能建领星出库单"})
		return
	}
	loaded := []shein.FulfillmentTask{task}
	if err := s.store.AttachOrderFulfillmentStates(ctx, shopKey, loaded); err != nil {
		s.internalError(writer, "attach SHEIN order state for XLWMS parcel create", err)
		return
	}
	task = loaded[0]
	if !shein.LabelPrintable(task) {
		writeJSON(writer, http.StatusConflict, response{Success: false, Error: "SHEIN 已揽收或已签收，不能再补建领星出库单"})
		return
	}
	parcel, err := s.createLingxingParcel(ctx, shopKey, orderNo, task, payload)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("XLWMS parcel create failed", "shop", shopKey, "error", sanitizedError(err))
		}
		if strings.Contains(err.Error(), "不完整") || strings.Contains(err.Error(), "必须与 DPS") || strings.Contains(err.Error(), "不能为空") {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		if strings.Contains(err.Error(), "已出库") || strings.Contains(err.Error(), "取消") {
			writeJSON(writer, http.StatusConflict, response{Success: false, Error: err.Error()})
			return
		}
		writeJSON(writer, http.StatusBadGateway, response{Success: false, Error: err.Error()})
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]any{
		"outbound_order_no":    parcel.OutboundOrderNo,
		"outbound_status":      parcel.Status,
		"outbound_status_name": firstNonEmpty(parcel.StatusName, ""),
		"label_attached":       parcel.LabelAttached,
		"can_upload_label":     lingxingParcelAllowsLabelUpdate(parcel),
		"upload_hint":          firstNonEmpty(lingxingLabelUploadHint(parcel), "出库单已创建并已带上 SHEIN 面单。进入仓库处理中后如需覆盖再补传。"),
	}})
}

func (s *Server) uploadXLWMSParcelLabel(writer http.ResponseWriter, request *http.Request) {
	if s.xlwms == nil {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "领星查询服务未配置"})
		return
	}
	orderNo := strings.TrimSpace(request.PathValue("orderNo"))
	if orderNo == "" {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "平台订单号不能为空"})
		return
	}
	var payload xlwmsParcelLabelUploadRequest
	if !decodeJSON(writer, request, &payload) {
		return
	}
	shopKey, err := s.requestedShopKey(request, payloadShopKeyFromRequest(request))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	task, err := s.store.LatestFulfillmentTask(ctx, shopKey, orderNo)
	if err != nil {
		if errors.Is(err, shein.ErrFulfillmentTaskNotFound) {
			writeJSON(writer, http.StatusNotFound, response{Success: false, Error: "当前店铺中未找到该履约任务"})
			return
		}
		s.internalError(writer, "load fulfillment task for XLWMS label upload", err)
		return
	}
	loaded := []shein.FulfillmentTask{task}
	if err := s.store.AttachOrderFulfillmentStates(ctx, shopKey, loaded); err != nil {
		s.internalError(writer, "attach SHEIN order state for XLWMS label upload", err)
		return
	}
	task = loaded[0]
	if !shein.LabelPrintable(task) {
		writeJSON(writer, http.StatusConflict, response{Success: false, Error: "SHEIN 已揽收或已签收，面单不能再打印"})
		return
	}
	if !shein.RequiresManualParcelCreate(shopKey, task.WarehouseAddressCode, "") {
		writeJSON(writer, http.StatusConflict, response{Success: false, Error: "当前发货仓不需要补传 DPS 面单"})
		return
	}
	if task.Status != "label_ready" && task.Status != "ready" {
		writeJSON(writer, http.StatusConflict, response{Success: false, Error: "当前订单尚未到面单阶段，不能上传面单"})
		return
	}
	warehouse := strings.ToUpper(strings.TrimSpace(firstNonEmpty(payload.Warehouse, shein.ResolvedDPSWarehouseCode(task.WarehouseAddressCode, ""))))
	if warehouse == "" {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "建单仓库不能为空"})
		return
	}
	parcel, err := s.lookupLingxingParcel(ctx, warehouse, orderNo)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, response{Success: false, Error: err.Error()})
		return
	}
	if !lingxingParcelAllowsLabelUpdate(parcel) {
		writeJSON(writer, http.StatusConflict, response{Success: false, Error: lingxingLabelUploadHint(parcel)})
		return
	}
	result, err := s.uploadLingxingParcelLabel(ctx, shopKey, orderNo, warehouse, task, payload.TrackingNumber, json.RawMessage(`{}`))
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("XLWMS parcel label upload failed", "shop", shopKey, "warehouse", warehouse, "error", sanitizedError(err))
		}
		if isLingxingLabelUpdateUnavailable(err) {
			writeJSON(writer, http.StatusConflict, response{Success: false, Error: lingxingLabelUploadHint(parcel)})
			return
		}
		writeJSON(writer, http.StatusBadGateway, response{Success: false, Error: err.Error()})
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, response{Success: true, Data: result})
}

func payloadShopKeyFromRequest(request *http.Request) string {
	return strings.TrimSpace(request.URL.Query().Get("shop_key"))
}

func (s *Server) uploadLingxingParcelLabel(
	ctx context.Context,
	shopKey, orderNo, warehouse string,
	task shein.FulfillmentTask,
	submittedTracking string,
	createResult json.RawMessage,
) (map[string]any, error) {
	labelURL, err := s.sheinLabelURL(ctx, shopKey, task)
	if err != nil {
		return nil, err
	}
	outboundOrderNo, err := s.resolveLingxingOutboundOrderNo(ctx, warehouse, orderNo, createResult)
	if err != nil {
		return nil, err
	}
	tracking := firstNonEmpty(strings.TrimSpace(submittedTracking), strings.TrimSpace(task.WaybillNo))
	payload := map[string]any{
		"outboundOrderNo": outboundOrderNo,
		"labelUrl":        labelURL,
		"labelFileName":   orderNo + ".pdf",
		"labelType":       "pdf",
	}
	if tracking != "" {
		payload["trackingNumber"] = tracking
	}
	result, err := s.xlwms.UpdateParcelTrackingLabel(ctx, warehouse, payload)
	if err != nil {
		return nil, err
	}
	if message := parcelLabelUploadFailure(result); message != "" {
		return nil, errors.New(message)
	}
	return map[string]any{
		"outbound_order_no": outboundOrderNo,
		"label_url":         labelURL,
		"tracking_number":   tracking,
		"result":            result,
	}, nil
}

func (s *Server) sheinLabelURL(ctx context.Context, shopKey string, task shein.FulfillmentTask) (string, error) {
	if s.store == nil {
		return "", errors.New("无法读取店铺凭证，不能获取 SHEIN 面单")
	}
	credentials, err := s.store.Credentials(ctx, shopKey)
	if err != nil {
		return "", err
	}
	data, err := printExpressRequest(task)
	if err != nil {
		return "", err
	}
	loaded := []shein.FulfillmentTask{task}
	if err := s.store.AttachOrderFulfillmentStates(ctx, shopKey, loaded); err != nil {
		return "", err
	}
	task = loaded[0]
	if !shein.LabelPrintable(task) {
		return "", errors.New("SHEIN 已揽收或已签收，面单不能再打印")
	}
	result, err := shein.NewClient(credentials, s.requestTimeout).Call(ctx, "print-express-info", data)
	if err != nil {
		return "", err
	}
	s.persistFulfillmentState(shopKey, "print-express-info", data, result)
	labelURL := firstSheinLabelURL(result)
	if labelURL == "" {
		return "", errors.New("SHEIN 未返回可上传的面单链接")
	}
	return labelURL, nil
}

func printExpressRequest(task shein.FulfillmentTask) (map[string]any, error) {
	if (task.OrderPlaceType != nil && *task.OrderPlaceType == 1) || strings.TrimSpace(task.DeliveryNo) == "" {
		packageNo := strings.TrimSpace(task.PackageNo)
		if strings.TrimSpace(task.OrderNo) == "" || packageNo == "" {
			return nil, errors.New("缺少订单号或包裹号，不能获取 SHEIN 面单")
		}
		return map[string]any{"orderNo": task.OrderNo, "packageNo": []any{packageNo}}, nil
	}
	return map[string]any{"deliveryNo": strings.TrimSpace(task.DeliveryNo)}, nil
}

func (s *Server) resolveLingxingOutboundOrderNo(ctx context.Context, warehouse, orderNo string, createResult json.RawMessage) (string, error) {
	if outbound := firstLingxingOutboundOrderNo(createResult); outbound != "" {
		return outbound, nil
	}
	parcel, err := s.lookupLingxingParcel(ctx, warehouse, orderNo)
	if err != nil {
		return "", err
	}
	if outbound := strings.TrimSpace(parcel.OutboundOrderNo); outbound != "" {
		return outbound, nil
	}
	return "", errors.New("领星未返回出库单号，无法上传面单")
}

func (s *Server) lookupLingxingParcel(ctx context.Context, warehouse, orderNo string) (lingxingParcelStatus, error) {
	if s.xlwms == nil {
		return lingxingParcelStatus{}, errors.New("领星查询服务未配置")
	}
	result, err := s.xlwms.LookupParcel(ctx, warehouse, orderNo, orderNo)
	if err != nil {
		return lingxingParcelStatus{}, err
	}
	return lingxingParcelStatusFromResult(result), nil
}

func (s *Server) ensureAutomaticDPSParcel(ctx context.Context, shopKey string, task shein.FulfillmentTask) (lingxingParcelStatus, error) {
	if s.xlwms == nil {
		return lingxingParcelStatus{}, errors.New("领星查询服务未配置")
	}
	if !shein.RequiresManualParcelCreate(shopKey, task.WarehouseAddressCode, "") {
		return lingxingParcelStatus{}, nil
	}
	loaded := []shein.FulfillmentTask{task}
	if s.store != nil {
		if err := s.store.AttachOrderFulfillmentStates(ctx, shopKey, loaded); err != nil {
			return lingxingParcelStatus{}, err
		}
		task = loaded[0]
	}
	if !shein.LabelPrintable(task) {
		return lingxingParcelStatus{}, errors.New("SHEIN 已揽收或已签收，不能再建领星出库单")
	}
	warehouse := shein.ResolvedDPSWarehouseCode(task.WarehouseAddressCode, "")
	if warehouse == "" {
		return lingxingParcelStatus{}, errors.New("DPS 发货仓无法识别")
	}
	parcel, err := s.lookupLingxingParcel(ctx, warehouse, task.OrderNo)
	if err != nil {
		return lingxingParcelStatus{}, err
	}
	if strings.TrimSpace(parcel.OutboundOrderNo) != "" {
		if parcel.LabelAttached || lingxingParcelCompletesManualCreate(parcel) {
			return parcel, nil
		}
		if lingxingParcelAllowsLabelUpdate(parcel) {
			if _, err := s.uploadLingxingParcelLabel(ctx, shopKey, task.OrderNo, warehouse, task, task.WaybillNo, json.RawMessage(`{}`)); err != nil {
				return parcel, err
			}
			return s.lookupLingxingParcel(ctx, warehouse, task.OrderNo)
		}
		if parcel.Status == nil || *parcel.Status != 0 {
			return parcel, errors.New(lingxingLabelUploadHint(parcel))
		}
	}
	draft, err := s.loadAutomaticParcelDraft(ctx, shopKey, task)
	if err != nil {
		return parcel, err
	}
	return s.createLingxingParcel(ctx, shopKey, task.OrderNo, task, parcelCreateRequestFromDraft(draft))
}

func (s *Server) loadAutomaticParcelDraft(ctx context.Context, shopKey string, task shein.FulfillmentTask) (xlwmsParcelDraft, error) {
	draft := parcelDraftFromTask(shopKey, s.shopName, task)
	draft.Required = true
	if s.store != nil {
		detail, err := s.store.OrderDetail(ctx, shopKey, task.OrderNo)
		if err != nil && !errors.Is(err, shein.ErrOrderNotFound) {
			return draft, err
		}
		applyOrderDetailToParcelDraft(&draft, detail)
	}
	if address, err := s.exportAddressForParcel(ctx, shopKey, task.OrderNo); err != nil {
		draft.MissingFields = appendUnique(draft.MissingFields, "收件地址")
	} else {
		applyAddressToParcelDraft(&draft, address)
	}
	applyMappedProductsToParcelDraft(ctx, s.store, shopKey, &draft, task.OrderNo)
	finalizeParcelDraft(&draft)
	if len(draft.MissingFields) > 0 {
		return draft, errors.New("DPS 自动建单缺少：" + strings.Join(draft.MissingFields, "、"))
	}
	return draft, nil
}

func parcelCreateRequestFromDraft(draft xlwmsParcelDraft) xlwmsParcelCreateRequest {
	return xlwmsParcelCreateRequest{
		Warehouse:         draft.Warehouse,
		PlatformOrderNo:   draft.OrderNo,
		ThirdOrderNo:      draft.OrderNo,
		SalesPlatform:     draft.SalesPlatform,
		StoreName:         draft.StoreName,
		ChannelCode:       draft.ChannelCode,
		TrackingNumber:    draft.TrackingNumber,
		Receiver:          draft.Receiver,
		Phone:             draft.Phone,
		CountryRegionCode: draft.CountryRegionCode,
		ProvinceCode:      draft.ProvinceCode,
		ProvinceName:      draft.ProvinceName,
		CityName:          draft.CityName,
		PostCode:          draft.PostCode,
		AddressOne:        draft.AddressOne,
		AddressTwo:        draft.AddressTwo,
		Products:          draft.Products,
	}
}

func (s *Server) createLingxingParcel(ctx context.Context, shopKey, orderNo string, task shein.FulfillmentTask, payload xlwmsParcelCreateRequest) (lingxingParcelStatus, error) {
	order, err := parcelCreateOrder(shopKey, s.shopName, orderNo, task, payload)
	if err != nil {
		return lingxingParcelStatus{}, err
	}
	warehouse := strings.ToUpper(strings.TrimSpace(payload.Warehouse))
	if err := s.cancelExistingLingxingParcel(ctx, warehouse, orderNo); err != nil {
		return lingxingParcelStatus{}, err
	}
	if err := s.attachSheinLabelToParcelOrder(ctx, shopKey, orderNo, task, order); err != nil {
		return lingxingParcelStatus{}, err
	}
	result, err := s.xlwms.CreateParcel(ctx, warehouse, []any{order})
	if err != nil {
		return lingxingParcelStatus{}, err
	}
	if message := parcelCreateFailure(result); message != "" {
		return lingxingParcelStatus{}, errors.New(message)
	}
	outboundOrderNo := firstLingxingOutboundOrderNo(result)
	if outboundOrderNo == "" {
		return lingxingParcelStatus{}, errors.New("领星建单成功但未返回出库单号，请刷新后再试")
	}
	parcel, _ := s.lookupLingxingParcel(ctx, warehouse, orderNo)
	if strings.TrimSpace(parcel.OutboundOrderNo) == "" {
		parcel.OutboundOrderNo = outboundOrderNo
	}
	if err := s.recordParcelWatch(ctx, shopKey, orderNo, warehouse, parcel); err != nil && s.logger != nil {
		s.logger.Warn("record SHEIN parcel watch after create failed", "shop", shopKey, "error", sanitizedError(err))
	}
	return parcel, nil
}

func (s *Server) attachSheinLabelToParcelOrder(ctx context.Context, shopKey, orderNo string, task shein.FulfillmentTask, order map[string]any) error {
	if order == nil {
		return errors.New("建单内容为空")
	}
	labelURL, err := s.sheinLabelURL(ctx, shopKey, task)
	if err != nil {
		return err
	}
	labelData, err := downloadSheinLabel(ctx, labelURL, s.requestTimeout)
	if err != nil {
		return err
	}
	attachLingxingLabelFiles(order, orderNo, labelURL, labelData)
	return nil
}

func (s *Server) cancelExistingLingxingParcel(ctx context.Context, warehouse, orderNo string) error {
	parcel, err := s.lookupLingxingParcel(ctx, warehouse, orderNo)
	if err != nil {
		return err
	}
	if strings.TrimSpace(parcel.OutboundOrderNo) == "" || (parcel.Status != nil && *parcel.Status == 4) {
		return nil
	}
	if parcel.Status != nil && *parcel.Status == 3 {
		return errors.New("出库单 " + parcel.OutboundOrderNo + " 已出库，不能取消后重建")
	}
	result, err := s.xlwms.CancelParcel(ctx, warehouse, []string{parcel.OutboundOrderNo})
	if err != nil {
		return err
	}
	if message := parcelCancelFailure(result); message != "" {
		return errors.New(message)
	}
	if err := s.waitForLingxingParcelCanceled(ctx, warehouse, parcel.OutboundOrderNo); err != nil {
		return err
	}
	return nil
}

func (s *Server) waitForLingxingParcelCanceled(ctx context.Context, warehouse, outboundOrderNo string) error {
	outboundOrderNo = strings.TrimSpace(outboundOrderNo)
	if outboundOrderNo == "" {
		return nil
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		status, err := s.xlwms.ParcelCancelStatus(ctx, warehouse, []string{outboundOrderNo})
		if err == nil {
			if message := parcelCancelStatusFailure(status, outboundOrderNo); message != "" {
				return errors.New(message)
			}
			if parcelCancelStatusSucceeded(status, outboundOrderNo) {
				return nil
			}
		}
		parcel, lookupErr := s.lookupLingxingParcelByOutbound(ctx, warehouse, outboundOrderNo)
		if lookupErr == nil && parcel.Status != nil && *parcel.Status == 4 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("出库单 " + outboundOrderNo + " 取消还在处理中，请稍后再重新建单")
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("出库单 " + outboundOrderNo + " 取消确认超时")
		case <-timer.C:
		}
	}
}

func (s *Server) lookupLingxingParcelByOutbound(ctx context.Context, warehouse, outboundOrderNo string) (lingxingParcelStatus, error) {
	if s.xlwms == nil {
		return lingxingParcelStatus{}, errors.New("领星查询服务未配置")
	}
	result, err := s.xlwms.LookupParcelByOutbound(ctx, warehouse, []string{outboundOrderNo})
	if err != nil {
		return lingxingParcelStatus{}, err
	}
	return lingxingParcelStatusFromResult(result), nil
}

func parcelCancelStatusSucceeded(result json.RawMessage, outboundOrderNo string) bool {
	status, ok := firstParcelCancelStatus(result, outboundOrderNo)
	return ok && status == 1
}

func parcelCancelStatusFailure(result json.RawMessage, outboundOrderNo string) string {
	status, ok := firstParcelCancelStatus(result, outboundOrderNo)
	if !ok || status != 2 {
		return ""
	}
	if message := extractFailureMessage(result); message != "" && message != "创建出库单失败" {
		return message
	}
	return "出库单 " + outboundOrderNo + " 取消失败"
}

func firstParcelCancelStatus(result json.RawMessage, outboundOrderNo string) (int, bool) {
	if len(result) == 0 {
		return 0, false
	}
	var decoded any
	if err := json.Unmarshal(result, &decoded); err != nil {
		return 0, false
	}
	return firstParcelCancelStatusValue(decoded, strings.TrimSpace(outboundOrderNo))
}

func firstParcelCancelStatusValue(value any, outboundOrderNo string) (int, bool) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if status, ok := firstParcelCancelStatusValue(item, outboundOrderNo); ok {
				return status, true
			}
		}
	case map[string]any:
		outbound := firstNonEmpty(firstString(typed, "outboundOrderNo", "orderNo"))
		if outbound != "" && outboundOrderNo != "" && outbound != outboundOrderNo {
			for _, key := range []string{"data", "info", "records"} {
				if status, ok := firstParcelCancelStatusValue(typed[key], outboundOrderNo); ok {
					return status, true
				}
			}
			return 0, false
		}
		if status, ok := integerValue(typed["status"]); ok && (outbound == outboundOrderNo || outboundOrderNo == "" || outbound == "") {
			return status, true
		}
		for _, key := range []string{"data", "info", "records"} {
			if status, ok := firstParcelCancelStatusValue(typed[key], outboundOrderNo); ok {
				return status, true
			}
		}
	case json.RawMessage:
		return firstParcelCancelStatus(typed, outboundOrderNo)
	}
	return 0, false
}

func parcelCancelFailure(result json.RawMessage) string {
	if message := parcelLabelUploadFailure(result); message != "" {
		return message
	}
	return extractFailureMessage(result)
}

func (s *Server) attachLingxingParcelStatusToTasks(ctx context.Context, shopKey string, tasks []shein.FulfillmentTask) {
	if s.xlwms == nil || !shein.RequiresManualParcelCreate(shopKey, "WH2604283535967233", "") {
		return
	}
	var wait sync.WaitGroup
	for index := range tasks {
		if !shein.RequiresManualParcelCreate(shopKey, tasks[index].WarehouseAddressCode, "") {
			continue
		}
		orderNo := strings.TrimSpace(tasks[index].OrderNo)
		warehouse := shein.ResolvedDPSWarehouseCode(tasks[index].WarehouseAddressCode, "")
		if orderNo == "" || warehouse == "" {
			continue
		}
		wait.Add(1)
		go func(index int, warehouse, orderNo string) {
			defer wait.Done()
			parcel, err := s.lookupLingxingParcel(ctx, warehouse, orderNo)
			if err != nil || strings.TrimSpace(parcel.OutboundOrderNo) == "" {
				return
			}
			tasks[index].OutboundOrderNo = parcel.OutboundOrderNo
			tasks[index].OutboundStatus = parcel.Status
			tasks[index].OutboundStatusName = parcel.StatusName
			tasks[index].LabelAttached = parcel.LabelAttached
			tasks[index].ParcelComplete = lingxingParcelCompletesManualCreate(parcel)
			if s.store != nil && (tasks[index].OMSSyncStatus == "" || tasks[index].OutboundOrderNo == "") {
				_ = s.recordParcelWatch(ctx, shopKey, orderNo, warehouse, parcel)
			}
		}(index, warehouse, orderNo)
	}
	wait.Wait()
}

func lingxingParcelCompletesManualCreate(parcel lingxingParcelStatus) bool {
	if strings.TrimSpace(parcel.OutboundOrderNo) == "" || parcel.Status == nil || !parcel.LabelAttached {
		return false
	}
	switch *parcel.Status {
	case 0, 1, 2, 3:
		return true
	default:
		return false
	}
}

func applyLingxingParcelStatusToDraft(ctx context.Context, client *xlwms.Client, draft *xlwmsParcelDraft) {
	if draft == nil || client == nil || strings.TrimSpace(draft.Warehouse) == "" || strings.TrimSpace(draft.OrderNo) == "" {
		return
	}
	result, err := client.LookupParcel(ctx, draft.Warehouse, draft.OrderNo, draft.OrderNo)
	if err != nil {
		draft.UploadHint = "无法核对领星出库单状态，补传前请先确认出库单已进入仓库处理中"
		return
	}
	parcel := lingxingParcelStatusFromResult(result)
	applyLingxingParcelToDraft(draft, parcel)
}

func applyLingxingParcelToDraft(draft *xlwmsParcelDraft, parcel lingxingParcelStatus) {
	if draft == nil || strings.TrimSpace(parcel.OutboundOrderNo) == "" {
		if draft != nil && draft.UploadHint == "" {
			draft.UploadHint = "还没有领星出库单。买完面单后会自动建单贴面单；失败后再点补建。"
		}
		return
	}
	draft.OutboundOrderNo = parcel.OutboundOrderNo
	draft.OutboundStatus = parcel.Status
	draft.OutboundStatusName = parcel.StatusName
	draft.LabelAttached = parcel.LabelAttached
	draft.CanUploadLabel = lingxingParcelAllowsLabelUpdate(parcel)
	draft.UploadHint = lingxingLabelUploadHint(parcel)
}

type lingxingParcelStatus struct {
	OutboundOrderNo string
	Status          *int
	StatusName      string
	Channel         string
	LabelAttached   bool
}

func lingxingParcelStatusFromResult(result json.RawMessage) lingxingParcelStatus {
	if len(result) == 0 {
		return lingxingParcelStatus{}
	}
	var decoded any
	if err := json.Unmarshal(result, &decoded); err != nil {
		return lingxingParcelStatus{}
	}
	return firstActiveLingxingParcel(decoded)
}

func firstActiveLingxingParcel(value any) lingxingParcelStatus {
	switch typed := value.(type) {
	case []any:
		var fallback lingxingParcelStatus
		for _, item := range typed {
			parcel := firstActiveLingxingParcel(item)
			if strings.TrimSpace(parcel.OutboundOrderNo) == "" {
				continue
			}
			if parcel.Status == nil || *parcel.Status != 4 {
				return parcel
			}
			if fallback.OutboundOrderNo == "" {
				fallback = parcel
			}
		}
		return fallback
	case map[string]any:
		if outbound := firstNonEmpty(firstString(typed, "outboundOrderNo")); outbound != "" {
			status, hasStatus := integerValue(typed["status"])
			parcel := lingxingParcelStatus{
				OutboundOrderNo: outbound,
				StatusName:      firstNonEmpty(firstString(typed, "statusName"), lingxingOutboundStatusName(status)),
				Channel:         firstString(typed, "logisticsChannel"),
				LabelAttached:   lingxingParcelHasLabel(typed),
			}
			if hasStatus {
				parcel.Status = &status
			}
			return parcel
		}
		for _, key := range []string{"data", "info", "records"} {
			if parcel := firstActiveLingxingParcel(typed[key]); parcel.OutboundOrderNo != "" {
				return parcel
			}
		}
	case json.RawMessage:
		return lingxingParcelStatusFromResult(typed)
	}
	return lingxingParcelStatus{}
}

func lingxingParcelHasLabel(value map[string]any) bool {
	if firstHTTPSURL(value, "fileUrl", "labelUrl") != "" {
		return true
	}
	for _, item := range objectItems(value["expressList"]) {
		if firstHTTPSURL(item, "fileUrl", "labelUrl", "returnUrl") != "" {
			return true
		}
	}
	return false
}

func lingxingParcelAllowsLabelUpdate(parcel lingxingParcelStatus) bool {
	if strings.TrimSpace(parcel.OutboundOrderNo) == "" || parcel.Status == nil {
		return false
	}
	if *parcel.Status != 2 && *parcel.Status != 7 {
		return false
	}
	channel := strings.TrimSpace(parcel.Channel)
	return channel == "" || strings.EqualFold(channel, lingxingPlatformLabelChannel)
}

func lingxingLabelUploadHint(parcel lingxingParcelStatus) string {
	if strings.TrimSpace(parcel.OutboundOrderNo) == "" {
		return "还没有领星出库单。买完面单后会自动建单贴面单；失败后再点补建。"
	}
	statusName := firstNonEmpty(parcel.StatusName, "未知状态")
	if parcel.Status != nil && *parcel.Status == 0 {
		if parcel.LabelAttached {
			return "出库单 " + parcel.OutboundOrderNo + " 还是草稿，面单已随建单提交。等它进入仓库处理中后，如需覆盖再点补传。"
		}
		return "出库单 " + parcel.OutboundOrderNo + " 还是草稿且没有面单。请重新建单，建单时会带上 SHEIN 面单。"
	}
	if parcel.Status != nil && *parcel.Status == 2 {
		if parcel.LabelAttached {
			return "出库单 " + parcel.OutboundOrderNo + " 已在仓库处理中，并且已有面单。需要覆盖时再点补传面单。"
		}
		return "出库单 " + parcel.OutboundOrderNo + " 已在仓库处理中，现在可以补传 SHEIN 面单。"
	}
	if parcel.Status != nil && *parcel.Status == 7 {
		return "出库单 " + parcel.OutboundOrderNo + " 当前是面单异常，可以再补传一次 SHEIN 面单。"
	}
	if parcel.Status != nil && (*parcel.Status == 4 || *parcel.Status == 5) {
		return "出库单 " + parcel.OutboundOrderNo + " 当前是" + statusName + "，不能补传。请重新建单后再补传。"
	}
	return "出库单 " + parcel.OutboundOrderNo + " 当前是" + statusName + "。领星只允许仓库处理中的自定义渠道出库单补传面单。"
}

func lingxingOutboundStatusName(status int) string {
	switch status {
	case 0:
		return "草稿"
	case 1:
		return "已取面单"
	case 2:
		return "仓库处理中"
	case 3:
		return "已出库"
	case 4:
		return "已取消"
	case 5:
		return "异常"
	case 6:
		return "拦截中"
	case 7:
		return "面单异常"
	default:
		return ""
	}
}

func firstSheinLabelURL(result map[string]any) string {
	for _, item := range objectItems(result["info"]) {
		if url := firstHTTPSURL(item, "filePdfUrl", "fileUrl", "packageLabel", "labelUrl"); url != "" {
			return url
		}
	}
	if url := firstHTTPSURL(firstObject(result["info"]), "filePdfUrl", "fileUrl", "packageLabel", "labelUrl"); url != "" {
		return url
	}
	return firstHTTPSURL(result, "filePdfUrl", "fileUrl", "packageLabel", "labelUrl")
}

func firstHTTPSURL(object map[string]any, keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(firstString(object, key))
		if strings.HasPrefix(strings.ToLower(value), "https://") {
			return value
		}
	}
	return ""
}

func firstLingxingOutboundOrderNo(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(result, &decoded); err != nil {
		return ""
	}
	return firstLingxingOutboundOrderNoValue(decoded)
}

func firstLingxingOutboundOrderNoValue(value any) string {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if outbound := firstLingxingOutboundOrderNoValue(item); outbound != "" {
				return outbound
			}
		}
	case map[string]any:
		if outbound := firstNonEmpty(firstString(typed, "outboundOrderNo", "orderNo")); outbound != "" {
			success, hasSuccess := typed["success"].(bool)
			if !hasSuccess || success {
				return outbound
			}
		}
		for _, key := range []string{"data", "info", "records"} {
			if outbound := firstLingxingOutboundOrderNoValue(typed[key]); outbound != "" {
				return outbound
			}
		}
	case json.RawMessage:
		return firstLingxingOutboundOrderNo(typed)
	}
	return ""
}

func parcelLabelUploadFailure(result json.RawMessage) string {
	var decoded any
	if err := json.Unmarshal(result, &decoded); err != nil {
		return ""
	}
	switch typed := decoded.(type) {
	case bool:
		if !typed {
			return "领星面单上传失败"
		}
	case map[string]any:
		if success, ok := typed["success"].(bool); ok && !success {
			return firstNonEmpty(firstString(typed, "msg", "message", "error"), "领星面单上传失败")
		}
		if data, ok := typed["data"].(bool); ok && !data {
			return firstNonEmpty(firstString(typed, "msg", "message", "error"), "领星面单上传失败")
		}
	}
	return ""
}

func parcelCreateOrder(shopKey, shopName, orderNo string, task shein.FulfillmentTask, payload xlwmsParcelCreateRequest) (map[string]any, error) {
	warehouse := strings.ToUpper(strings.TrimSpace(payload.Warehouse))
	expected := shein.ResolvedDPSWarehouseCode(task.WarehouseAddressCode, "")
	if expected != "" && warehouse != expected {
		return nil, errors.New("建单仓库必须与 DPS 发货仓一致")
	}
	products := parcelDraftProductList(payload.Products)
	if warehouse == "" || strings.TrimSpace(payload.Receiver) == "" || strings.TrimSpace(payload.AddressOne) == "" ||
		strings.TrimSpace(payload.CountryRegionCode) == "" || strings.TrimSpace(payload.ProvinceName) == "" ||
		strings.TrimSpace(payload.CityName) == "" || strings.TrimSpace(payload.PostCode) == "" ||
		strings.TrimSpace(payload.ChannelCode) == "" || len(products) == 0 {
		return nil, errors.New("建单字段不完整")
	}
	platformOrderNo := firstNonEmpty(strings.TrimSpace(payload.PlatformOrderNo), orderNo)
	salesPlatform := firstNonEmpty(strings.TrimSpace(payload.SalesPlatform), "SHEIN")
	storeName := firstNonEmpty(strings.TrimSpace(payload.StoreName), defaultLingxingStoreName(shopKey), strings.TrimSpace(shopName))
	if salesPlatform == "" || storeName == "" {
		return nil, errors.New("销售平台和店铺不能为空")
	}
	order := map[string]any{
		"whCode":            warehouse,
		"platformOrderNo":   platformOrderNo,
		"thirdOrderNo":      platformOrderNo,
		"salesPlatform":     salesPlatform,
		"store":             storeName,
		"storeName":         storeName,
		"subOrderType":      1,
		"logisticsChannel":  lingxingParcelChannel(task.ExpressChannelCode, payload.ChannelCode),
		"receiver":          strings.TrimSpace(payload.Receiver),
		"countryRegionCode": strings.ToUpper(strings.TrimSpace(payload.CountryRegionCode)),
		"provinceCode":      firstNonEmpty(strings.TrimSpace(payload.ProvinceCode), strings.TrimSpace(payload.ProvinceName)),
		"provinceName":      strings.TrimSpace(payload.ProvinceName),
		"cityName":          strings.TrimSpace(payload.CityName),
		"postCode":          strings.TrimSpace(payload.PostCode),
		"addressOne":        strings.TrimSpace(payload.AddressOne),
		"productList":       products,
	}
	if phone := strings.TrimSpace(payload.Phone); phone != "" {
		order["telephone"] = phone
		order["phone"] = phone
	}
	if addressTwo := strings.TrimSpace(payload.AddressTwo); addressTwo != "" {
		order["addressTwo"] = addressTwo
	}
	if tracking := strings.TrimSpace(payload.TrackingNumber); tracking != "" {
		order["logisticsTrackNo"] = tracking
	}
	return order, nil
}

func attachLingxingLabelFiles(order map[string]any, orderNo, labelURL string, labelData []byte) {
	if order == nil || len(labelData) == 0 {
		return
	}
	fileName := strings.TrimSpace(orderNo) + ".pdf"
	encoded := base64.StdEncoding.EncodeToString(labelData)
	order["fileList"] = []any{map[string]any{
		"fileName": fileName,
		"fileData": encoded,
		"fileType": "pdf",
		"bizCode":  "1",
	}}
	order["label"] = map[string]any{
		"fileName": fileName,
		"fileType": "pdf",
		"fileData": encoded,
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(labelURL)), "https://") {
		order["labelUrl"] = strings.TrimSpace(labelURL)
	}
}

func downloadSheinLabel(ctx context.Context, labelURL string, timeout time.Duration) ([]byte, error) {
	labelURL = strings.TrimSpace(labelURL)
	if !strings.HasPrefix(strings.ToLower(labelURL), "https://") {
		return nil, errors.New("SHEIN 面单链接无效")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, labelURL, nil)
	if err != nil {
		return nil, errors.New("无法下载 SHEIN 面单")
	}
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("无法下载 SHEIN 面单")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("SHEIN 面单链接已失效，请重新获取面单后再建单")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil || len(raw) == 0 {
		return nil, errors.New("SHEIN 面单文件为空")
	}
	if !isPDF(raw) {
		return nil, errors.New("SHEIN 返回的不是可上传的 PDF 面单")
	}
	return raw, nil
}

func isPDF(raw []byte) bool {
	return len(raw) >= 4 && string(raw[:4]) == "%PDF"
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func isLingxingLabelUpdateUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "仅状态=仓库处理中") || strings.Contains(message, "渠道类型=自定义渠道")
}

func finalizeParcelDraft(draft *xlwmsParcelDraft) {
	if draft == nil {
		return
	}
	required := map[string]string{
		"建单仓库": draft.Warehouse,
		"销售平台": draft.SalesPlatform,
		"店铺":   draft.StoreName,
		"物流渠道": draft.ChannelCode,
		"收件人":  draft.Receiver,
		"国家":   draft.CountryRegionCode,
		"省州":   draft.ProvinceName,
		"城市":   draft.CityName,
		"邮编":   draft.PostCode,
		"地址":   draft.AddressOne,
	}
	for label, value := range required {
		if strings.TrimSpace(value) == "" {
			draft.MissingFields = appendUnique(draft.MissingFields, label)
		}
	}
	if len(draft.Products) == 0 {
		draft.MissingFields = appendUnique(draft.MissingFields, "商品明细")
	}
	if draft.ProvinceCode == "" {
		draft.ProvinceCode = draft.ProvinceName
	}
	draft.Ready = draft.Required && len(draft.MissingFields) == 0
}

func parcelDraftFromTask(shopKey, shopName string, task shein.FulfillmentTask) xlwmsParcelDraft {
	warehouse := shein.ResolvedDPSWarehouseCode(task.WarehouseAddressCode, "")
	required := shein.RequiresManualParcelCreate(shopKey, task.WarehouseAddressCode, "") && shein.LabelPrintable(task) &&
		(!task.ParcelComplete || (task.OutboundStatus != nil && *task.OutboundStatus == 7) ||
			(strings.TrimSpace(task.OutboundOrderNo) != "" && !task.LabelAttached))
	draft := xlwmsParcelDraft{
		Required:       required,
		Warehouse:      warehouse,
		WarehouseHint:  strings.TrimSpace(task.WarehouseAddressCode),
		OrderNo:        strings.TrimSpace(task.OrderNo),
		SalesPlatform:  "SHEIN",
		StoreName:      firstNonEmpty(defaultLingxingStoreName(shopKey), strings.TrimSpace(shopName)),
		ChannelCode:    lingxingPlatformLabelChannel,
		ChannelHint:    strings.TrimSpace(task.ExpressChannelCode),
		TrackingNumber: strings.TrimSpace(task.WaybillNo),
		Products:       []xlwmsParcelDraftProduct{},
		MissingFields:  []string{},
	}
	if required {
		draft.Reason = "当前 SHEIN 店铺发往 DPS 仓时，买完面单后会自动建领星出库单并贴上 SHEIN 面单；这里只用于失败后补建或补传"
		draft.UploadHint = "还没有领星出库单。买完面单后会自动建单贴面单；失败后再点补建。"
	}
	if required && warehouse == "" {
		draft.MissingFields = append(draft.MissingFields, "建单仓库")
	}
	if required && draft.ChannelCode == "" {
		draft.MissingFields = append(draft.MissingFields, "物流渠道")
	}
	if required && draft.StoreName == "" {
		draft.MissingFields = append(draft.MissingFields, "店铺")
	}
	return draft
}

func applyOrderDetailToParcelDraft(draft *xlwmsParcelDraft, detail map[string]any) {
	if draft == nil || len(detail) == 0 {
		return
	}
	receive := firstObject(detail["receiveMsg"])
	if draft.Receiver == "" {
		draft.Receiver = receiverNameFromAddress(receive)
	}
	if draft.Phone == "" {
		draft.Phone = firstNonEmpty(firstString(receive, "phone"), firstString(receive, "mobile"), firstString(receive, "tel"))
	}
	if draft.CountryRegionCode == "" {
		draft.CountryRegionCode = normalizeCountryCode(firstNonEmpty(firstString(receive, "countryCode"), firstString(receive, "country")))
	}
	if draft.ProvinceName == "" {
		draft.ProvinceName = firstString(receive, "province")
	}
	if draft.ProvinceCode == "" {
		draft.ProvinceCode = firstNonEmpty(firstString(receive, "provinceCode"), draft.ProvinceName)
	}
	if draft.CityName == "" {
		draft.CityName = firstString(receive, "city")
	}
	if draft.PostCode == "" {
		draft.PostCode = firstString(receive, "postCode")
	}
	if draft.AddressOne == "" {
		draft.AddressOne = firstNonEmpty(firstString(receive, "address"), firstString(receive, "address1"), firstString(receive, "addressOne"))
	}
	if draft.AddressTwo == "" {
		draft.AddressTwo = firstNonEmpty(firstString(receive, "address2"), firstString(receive, "addressTwo"))
	}
}

func applyAddressToParcelDraft(draft *xlwmsParcelDraft, address map[string]any) {
	if draft == nil || len(address) == 0 {
		return
	}
	draft.Receiver = firstNonEmpty(receiverNameFromAddress(address), draft.Receiver)
	draft.Phone = firstNonEmpty(firstString(address, "phone", "mobile", "tel"), draft.Phone)
	draft.CountryRegionCode = firstNonEmpty(normalizeCountryCode(firstString(address, "countryCode", "country")), draft.CountryRegionCode)
	draft.ProvinceName = firstNonEmpty(firstString(address, "province", "provinceName"), draft.ProvinceName)
	draft.ProvinceCode = firstNonEmpty(firstString(address, "provinceCode"), draft.ProvinceCode, draft.ProvinceName)
	draft.CityName = firstNonEmpty(firstString(address, "city", "cityName"), draft.CityName)
	draft.PostCode = firstNonEmpty(firstString(address, "postCode", "zipCode"), draft.PostCode)
	draft.AddressOne = firstNonEmpty(firstString(address, "address", "address1", "addressOne"), draft.AddressOne)
	draft.AddressTwo = firstNonEmpty(firstString(address, "address2", "addressTwo"), draft.AddressTwo)
}

func applyMappedProductsToParcelDraft(ctx context.Context, store *shein.Store, shopKey string, draft *xlwmsParcelDraft, orderNo string) {
	if store == nil || draft == nil {
		return
	}
	goods, err := store.MappedOrderGoods(ctx, shopKey, orderNo)
	if err != nil {
		draft.MissingFields = appendUnique(draft.MissingFields, "商品明细")
		return
	}
	products := make([]xlwmsParcelDraftProduct, 0, len(goods))
	for _, item := range goods {
		sku := strings.TrimSpace(item.WarehouseSKU)
		if sku == "" {
			draft.MissingFields = appendUnique(draft.MissingFields, "仓库 SKU")
			continue
		}
		quantity := 1
		if raw := strings.TrimSpace(item.WarehouseQuantity); raw != "" {
			if parsed, parseErr := strconv.ParseFloat(raw, 64); parseErr == nil && parsed > 0 {
				quantity = int(math.Ceil(parsed))
			}
		}
		products = append(products, xlwmsParcelDraftProduct{SKU: sku, Quantity: quantity})
	}
	draft.Products = mergeParcelDraftProducts(products)
	if len(draft.Products) == 0 {
		draft.MissingFields = appendUnique(draft.MissingFields, "商品明细")
	}
}

func defaultLingxingStoreName(shopKey string) string {
	switch strings.TrimSpace(shopKey) {
	case "", "default", "beauty-hangers-home":
		return "BeautyHanger"
	default:
		return ""
	}
}

func receiverNameFromAddress(address map[string]any) string {
	if joined := joinPersonName(
		firstString(address, "firstName"),
		firstString(address, "middleName"),
		firstString(address, "lastName"),
	); joined != "" {
		return joined
	}
	return firstString(address, "fullName", "receiverName", "receiver", "name")
}

func joinPersonName(parts ...string) string {
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			items = append(items, value)
		}
	}
	return strings.Join(items, " ")
}

func lingxingParcelChannel(sheinChannel, submitted string) string {
	submitted = strings.TrimSpace(submitted)
	sheinChannel = strings.TrimSpace(sheinChannel)
	if submitted == "" || strings.EqualFold(submitted, sheinChannel) ||
		strings.EqualFold(submitted, lingxingPlatformLabelChannel) || isSheinExpressChannel(submitted) {
		return lingxingPlatformLabelChannel
	}
	return submitted
}

func isSheinExpressChannel(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	upper := strings.ToUpper(value)
	return strings.HasSuffix(upper, "-NA") || strings.Contains(upper, "D2D")
}

func parcelCreateFailure(result json.RawMessage) string {
	var envelope struct {
		Msg  string `json:"msg"`
		Data any    `json:"data"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return firstNonEmpty(extractFailureMessage(result), "")
	}
	if message := extractFailureMessage(envelope.Data); message != "" {
		return message
	}
	if message := strings.TrimSpace(envelope.Msg); message != "" && message != "操作成功" && message != "ok" {
		return message
	}
	return ""
}

func extractFailureMessage(value any) string {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if message := extractFailureMessage(item); message != "" {
				return message
			}
		}
	case map[string]any:
		success, hasSuccess := typed["success"].(bool)
		if hasSuccess && !success {
			if message := firstNonEmpty(firstString(typed, "msg", "message", "error"), firstString(typed, "failReason")); message != "" {
				return message
			}
			return "创建出库单失败"
		}
		if message := extractFailureMessage(typed["data"]); message != "" {
			return message
		}
	case json.RawMessage:
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err == nil {
			return extractFailureMessage(decoded)
		}
	}
	return ""
}

func mergeParcelDraftProducts(products []xlwmsParcelDraftProduct) []xlwmsParcelDraftProduct {
	merged := make([]xlwmsParcelDraftProduct, 0, len(products))
	indexBySKU := map[string]int{}
	for _, product := range products {
		sku := strings.TrimSpace(product.SKU)
		if sku == "" || product.Quantity < 1 {
			continue
		}
		if index, ok := indexBySKU[sku]; ok {
			merged[index].Quantity += product.Quantity
			continue
		}
		indexBySKU[sku] = len(merged)
		merged = append(merged, xlwmsParcelDraftProduct{SKU: sku, Quantity: product.Quantity})
	}
	return merged
}

func parcelDraftProductList(products []xlwmsParcelDraftProduct) []any {
	items := make([]any, 0, len(products))
	for _, product := range products {
		sku := strings.TrimSpace(product.SKU)
		if sku == "" || product.Quantity < 1 {
			continue
		}
		items = append(items, map[string]any{"sku": sku, "quantity": product.Quantity})
	}
	return items
}

func normalizeCountryCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch value {
	case "UNITED STATES", "UNITED STATES OF AMERICA", "USA", "US":
		return "US"
	default:
		if len(value) == 2 {
			return value
		}
		return value
	}
}

func (s *Server) exportAddressForParcel(ctx context.Context, shopKey, orderNo string) (map[string]any, error) {
	credentials, err := s.store.Credentials(ctx, shopKey)
	if err != nil {
		return nil, err
	}
	result, err := shein.NewClient(credentials, s.requestTimeout).Call(ctx, "export-address", map[string]any{
		"orderNo": orderNo, "handleType": 1,
	})
	if err != nil {
		return nil, err
	}
	info := firstObject(result["info"])
	if address := firstObject(info["receiveMsg"]); len(address) > 0 {
		return address, nil
	}
	if items := objectItems(info["receiveMsgList"]); len(items) > 0 {
		return items[0], nil
	}
	if len(info) > 0 {
		return info, nil
	}
	return nil, errors.New("export-address did not return a receive address")
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
	decision, queryErr := s.xlwms.QueryInventoryForShop(ctx, "shein", shopKey, items)
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

func (s *Server) inventoryThresholds(writer http.ResponseWriter, request *http.Request) {
	if s.xlwms == nil {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "领星查询服务未配置"})
		return
	}
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	page := queryInt(request, "page", 1)
	pageSize := queryInt(request, "page_size", 30)
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	item, err := s.xlwms.ListShopSKUInventoryThresholds(ctx, "shein", shopKey, request.URL.Query().Get("q"), page, pageSize)
	if err != nil {
		writeXLWMSError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: item.Records, Meta: map[string]any{
		"page": item.Page, "page_size": item.PageSize, "total": item.Total, "pages": item.Pages,
		"default_thresholds": item.DefaultThresholds,
	}})
}

func (s *Server) inventoryThresholdDefaults(writer http.ResponseWriter, request *http.Request) {
	if s.xlwms == nil {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "领星查询服务未配置"})
		return
	}
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	if request.Method == http.MethodGet {
		item, err := s.xlwms.ShopInventoryThresholds(ctx, "shein", shopKey)
		if err != nil {
			writeXLWMSError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, response{Success: true, Data: item})
		return
	}
	var payload xlwms.InventoryThresholds
	if !decodeJSON(writer, request, &payload) {
		return
	}
	item, err := s.xlwms.UpdateShopInventoryThresholds(ctx, "shein", shopKey, payload)
	if err != nil {
		writeXLWMSError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) resetInventoryThresholdDefaults(writer http.ResponseWriter, request *http.Request) {
	if s.xlwms == nil {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "领星查询服务未配置"})
		return
	}
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	item, err := s.xlwms.ResetShopInventoryThresholds(ctx, "shein", shopKey)
	if err != nil {
		writeXLWMSError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) updateSKUInventoryThreshold(writer http.ResponseWriter, request *http.Request) {
	if s.xlwms == nil {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "领星查询服务未配置"})
		return
	}
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	var payload xlwms.InventoryThresholds
	if !decodeJSON(writer, request, &payload) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	item, err := s.xlwms.UpdateShopSKUInventoryThreshold(ctx, "shein", shopKey, request.PathValue("warehouseSKU"), payload)
	if err != nil {
		writeXLWMSError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) resetSKUInventoryThreshold(writer http.ResponseWriter, request *http.Request) {
	if s.xlwms == nil {
		writeJSON(writer, http.StatusServiceUnavailable, response{Success: false, Error: "领星查询服务未配置"})
		return
	}
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	if err := s.xlwms.ResetShopSKUInventoryThreshold(ctx, "shein", shopKey, request.PathValue("warehouseSKU")); err != nil {
		writeXLWMSError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]bool{"deleted": true}})
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
