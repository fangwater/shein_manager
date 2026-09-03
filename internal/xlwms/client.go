package xlwms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

type Client struct {
	baseURL string
	http    *http.Client
}

type GatewayError struct {
	StatusCode int
	Message    string
}

func (err *GatewayError) Error() string { return err.Message }

type Account struct {
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	WarehouseCodes []string `json:"warehouse_codes"`
	Available      bool     `json:"available"`
	Status         string   `json:"status,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type PlatformOrder struct {
	OMSOrderNo         string `json:"oms_order_no"`
	PlatformOrderNo    string `json:"platform_order_no"`
	PlatformCode       string `json:"platform_code,omitempty"`
	Status             int    `json:"status"`
	StatusKey          string `json:"status_key"`
	StatusText         string `json:"status_text,omitempty"`
	SubStatus          int    `json:"sub_status"`
	SendWarehouseCode  string `json:"send_warehouse_code,omitempty"`
	TrackingNumber     string `json:"tracking_number,omitempty"`
	OrderTime          string `json:"order_time,omitempty"`
	CreateTime         string `json:"create_time,omitempty"`
	AuditTime          string `json:"audit_time,omitempty"`
	MarkShipmentStatus int    `json:"mark_shipment_status"`
	MarkShipmentTime   string `json:"mark_shipment_time,omitempty"`
}

type PlatformOrderLookup struct {
	Account         string          `json:"account"`
	PlatformOrderNo string          `json:"platform_order_no"`
	Found           bool            `json:"found"`
	MatchCount      int             `json:"match_count"`
	Orders          []PlatformOrder `json:"orders"`
	QueriedAt       time.Time       `json:"queried_at"`
}

type InventoryItem struct {
	WarehouseSKU string `json:"warehouse_sku"`
	Quantity     int    `json:"quantity"`
}

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
	WarehouseSKU string                `json:"warehouse_sku,omitempty"`
	Customized   bool                  `json:"customized"`
	Source       string                `json:"source"`
	BaseRules    WarehouseCarrierRules `json:"base_rules"`
	Carriers     []CarrierPolicy       `json:"carriers"`
}

func (client *Client) CarrierPolicies(ctx context.Context, platform, warehouseSKU string) ([]WarehouseCarrierPolicies, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return nil, errors.New("platform is required")
	}
	values := url.Values{}
	values.Set("platform", platform)
	if warehouseSKU = strings.TrimSpace(warehouseSKU); warehouseSKU != "" {
		values.Set("warehouse_sku", warehouseSKU)
	}
	var result []WarehouseCarrierPolicies
	if err := client.do(ctx, http.MethodGet, "/fulfillment-policies/carriers?"+values.Encode(), nil, "", "", "", &result); err != nil {
		return nil, err
	}
	return result, nil
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
}

func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("XLWMS_BASE_URL must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("XLWMS_BASE_URL must use HTTP or HTTPS")
	}
	if timeout <= 0 {
		return nil, errors.New("XLWMS request timeout must be positive")
	}
	return &Client{baseURL: baseURL, http: &http.Client{Timeout: timeout}}, nil
}

func (client *Client) Accounts(ctx context.Context) ([]Account, error) {
	var accounts []Account
	if err := client.do(ctx, http.MethodGet, "/platform-orders/accounts", nil, "", "", "", &accounts); err != nil {
		return nil, err
	}
	if accounts == nil {
		accounts = []Account{}
	}
	return accounts, nil
}

type FulfillmentAuditSnapshotOrder struct {
	PlatformOrderNo    string     `json:"platform_order_no"`
	PlatformStatus     string     `json:"platform_status"`
	PlatformStatusCode *int       `json:"platform_status_code,omitempty"`
	PlatformShippingAt *time.Time `json:"platform_shipping_at,omitempty"`
	WarehouseKey       string     `json:"warehouse_key"`
	WarehouseCode      string     `json:"wh_code"`
	TrackingNumber     string     `json:"tracking_number"`
}

type FulfillmentAuditSnapshot struct {
	Platform string                          `json:"platform"`
	ShopCode string                          `json:"shop_code"`
	ShopName string                          `json:"shop_name"`
	Orders   []FulfillmentAuditSnapshotOrder `json:"orders"`
}

func (client *Client) SyncFulfillmentAudits(ctx context.Context, snapshot FulfillmentAuditSnapshot) error {
	if strings.TrimSpace(snapshot.Platform) == "" || strings.TrimSpace(snapshot.ShopCode) == "" {
		return errors.New("platform and shop_code are required")
	}
	if snapshot.Orders == nil {
		snapshot.Orders = []FulfillmentAuditSnapshotOrder{}
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	var result map[string]any
	return client.do(ctx, http.MethodPost, "/fulfillment-audits/sync", body, "", snapshot.Platform, snapshot.ShopCode, &result)
}

const (
	AutoMatchCarrier = "_AUTO_MATCH_"
	OtherCarrier     = "other"
)

type WarehouseAssignmentRoute struct {
	PlatformOrderNo     string `json:"platform_order_no"`
	PlatformWarehouseID string `json:"platform_warehouse_id"`
	PlatformWarehouse   string `json:"platform_warehouse_name"`
	WarehouseCode       string `json:"warehouse_code"`
	WarehouseName       string `json:"warehouse_name"`
}

type WarehouseAssignmentFailure struct {
	PlatformOrderNo string `json:"platform_order_no"`
	Error           string `json:"error"`
}

type WarehouseAssignmentCarrier struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type WarehouseAssignmentUnresolved struct {
	PlatformOrderNo string `json:"platform_order_no"`
	Reason          string `json:"reason"`
}

type WarehouseAssignmentPreview struct {
	Ready       bool                            `json:"ready"`
	Routes      []WarehouseAssignmentRoute      `json:"routes"`
	Unresolved  []WarehouseAssignmentUnresolved `json:"unresolved"`
	ChannelCode string                          `json:"channel_code"`
	ChannelName string                          `json:"channel_name"`
	Carriers    []WarehouseAssignmentCarrier    `json:"carriers"`
	QueriedAt   time.Time                       `json:"queried_at"`
}

type WarehouseAssignmentResult struct {
	Account          string                       `json:"account"`
	Total            int                          `json:"total"`
	Success          int                          `json:"success"`
	Failed           int                          `json:"failed"`
	Failures         []WarehouseAssignmentFailure `json:"failures"`
	Routes           []WarehouseAssignmentRoute   `json:"routes"`
	WarehouseCode    string                       `json:"warehouse_code"`
	WarehouseCodes   []string                     `json:"warehouse_codes"`
	ChannelCode      string                       `json:"channel_code"`
	LogisticsCarrier string                       `json:"logistics_carrier"`
	CompletedAt      time.Time                    `json:"completed_at"`
}

func (client *Client) PreviewWarehouseAssignment(ctx context.Context, account, platformOrderNo string) (WarehouseAssignmentPreview, error) {
	account, platformOrderNo, err := validateWarehouseAssignmentTarget(account, platformOrderNo)
	if err != nil {
		return WarehouseAssignmentPreview{}, err
	}
	body, err := json.Marshal(map[string]any{"platform_order_nos": []string{platformOrderNo}})
	if err != nil {
		return WarehouseAssignmentPreview{}, err
	}
	var result WarehouseAssignmentPreview
	if err := client.do(ctx, http.MethodPost, "/platform-orders/routing-preview", body, account, "", "", &result); err != nil {
		return WarehouseAssignmentPreview{}, err
	}
	if result.Routes == nil {
		result.Routes = []WarehouseAssignmentRoute{}
	}
	if result.Unresolved == nil {
		result.Unresolved = []WarehouseAssignmentUnresolved{}
	}
	if result.Carriers == nil {
		result.Carriers = []WarehouseAssignmentCarrier{}
	}
	return result, nil
}

func (client *Client) AssignWarehouse(ctx context.Context, account, platformOrderNo, logisticsCarrier string) (WarehouseAssignmentResult, error) {
	account, platformOrderNo, err := validateWarehouseAssignmentTarget(account, platformOrderNo)
	if err != nil {
		return WarehouseAssignmentResult{}, err
	}
	logisticsCarrier = strings.TrimSpace(logisticsCarrier)
	if logisticsCarrier != AutoMatchCarrier && logisticsCarrier != OtherCarrier {
		return WarehouseAssignmentResult{}, errors.New("logistics carrier must be automatic matching or Other")
	}
	body, err := json.Marshal(map[string]any{
		"platform_order_nos": []string{platformOrderNo},
		"logistics_carrier":  logisticsCarrier,
		"confirmation":       "CONFIRM_AND_APPROVE",
	})
	if err != nil {
		return WarehouseAssignmentResult{}, err
	}
	var result WarehouseAssignmentResult
	if err := client.do(ctx, http.MethodPost, "/platform-orders/warehouse-assignments", body, account, "", "", &result); err != nil {
		return WarehouseAssignmentResult{}, err
	}
	if result.Failures == nil {
		result.Failures = []WarehouseAssignmentFailure{}
	}
	if result.Routes == nil {
		result.Routes = []WarehouseAssignmentRoute{}
	}
	if result.WarehouseCodes == nil {
		result.WarehouseCodes = []string{}
	}
	return result, nil
}

func validateWarehouseAssignmentTarget(account, platformOrderNo string) (string, string, error) {
	account = strings.TrimSpace(account)
	platformOrderNo = strings.TrimSpace(platformOrderNo)
	if account == "" {
		return "", "", errors.New("OMS account is required")
	}
	if platformOrderNo == "" {
		return "", "", errors.New("platform order number is required")
	}
	return account, platformOrderNo, nil
}

func (client *Client) QueryPlatformOrder(ctx context.Context, account, platformOrderNo string) (PlatformOrderLookup, error) {
	account = strings.TrimSpace(account)
	platformOrderNo = strings.TrimSpace(platformOrderNo)
	if account == "" {
		return PlatformOrderLookup{}, errors.New("OMS account is required")
	}
	if platformOrderNo == "" {
		return PlatformOrderLookup{}, errors.New("platform order number is required")
	}
	var result PlatformOrderLookup
	path := "/temu/platform-orders/" + url.PathEscape(platformOrderNo)
	if err := client.do(ctx, http.MethodGet, path, nil, account, "", "", &result); err != nil {
		return PlatformOrderLookup{}, err
	}
	if result.Orders == nil {
		result.Orders = []PlatformOrder{}
	}
	return result, nil
}

func (client *Client) QueryInventory(ctx context.Context, items []InventoryItem) (json.RawMessage, error) {
	return client.QueryInventoryForShop(ctx, "", "", items)
}

func (client *Client) QueryInventoryForShop(ctx context.Context, platform, shopCode string, items []InventoryItem) (json.RawMessage, error) {
	if len(items) == 0 {
		return nil, errors.New("at least one warehouse SKU is required")
	}
	payload := map[string]any{"items": items}
	if platform = strings.TrimSpace(platform); platform != "" {
		payload["platform"] = platform
	}
	if shopCode = strings.TrimSpace(shopCode); shopCode != "" {
		payload["shop_code"] = shopCode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err := client.do(ctx, http.MethodPost, "/temu/warehouse-availability/query", body, "", platform, shopCode, &result); err != nil {
		return nil, err
	}
	return result, nil
}

type InventoryThresholds struct {
	EastThreshold  float64 `json:"east_threshold"`
	WestThreshold  float64 `json:"west_threshold"`
	TotalThreshold float64 `json:"total_threshold"`
}

type PlatformInventoryThresholds struct {
	Platform       string    `json:"platform"`
	EastThreshold  float64   `json:"east_threshold"`
	WestThreshold  float64   `json:"west_threshold"`
	TotalThreshold float64   `json:"total_threshold"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type SKUInventoryThreshold struct {
	WarehouseSKU   string     `json:"warehouse_sku"`
	ProductName    string     `json:"product_name"`
	EastAvailable  float64    `json:"east_available"`
	WestAvailable  float64    `json:"west_available"`
	TotalAvailable float64    `json:"total_available"`
	EastThreshold  float64    `json:"east_threshold"`
	WestThreshold  float64    `json:"west_threshold"`
	TotalThreshold float64    `json:"total_threshold"`
	Customized     bool       `json:"customized"`
	Source         string     `json:"source,omitempty"`
	InventoryAt    *time.Time `json:"inventory_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type InventoryThresholdPage struct {
	Records           []SKUInventoryThreshold     `json:"records"`
	Total             int                         `json:"total"`
	Page              int                         `json:"page"`
	PageSize          int                         `json:"page_size"`
	Pages             int                         `json:"pages"`
	DefaultThresholds PlatformInventoryThresholds `json:"default_thresholds"`
}

func (client *Client) PlatformInventoryThresholds(ctx context.Context, platform string) (PlatformInventoryThresholds, error) {
	platform = strings.TrimSpace(strings.ToLower(platform))
	if platform == "" {
		return PlatformInventoryThresholds{}, errors.New("platform is required")
	}
	var result PlatformInventoryThresholds
	if err := client.do(ctx, http.MethodGet, "/inventory-thresholds/defaults?platform="+url.QueryEscape(platform), nil, "", "", "", &result); err != nil {
		return PlatformInventoryThresholds{}, err
	}
	return result, nil
}

func (client *Client) UpdatePlatformInventoryThresholds(ctx context.Context, platform string, thresholds InventoryThresholds) (PlatformInventoryThresholds, error) {
	platform = strings.TrimSpace(strings.ToLower(platform))
	if platform == "" {
		return PlatformInventoryThresholds{}, errors.New("platform is required")
	}
	body, err := json.Marshal(thresholds)
	if err != nil {
		return PlatformInventoryThresholds{}, err
	}
	var result PlatformInventoryThresholds
	if err := client.do(ctx, http.MethodPatch, "/inventory-thresholds/defaults?platform="+url.QueryEscape(platform), body, "", "", "", &result); err != nil {
		return PlatformInventoryThresholds{}, err
	}
	return result, nil
}

func (client *Client) ListPlatformSKUInventoryThresholds(ctx context.Context, platform, query string, page, pageSize int) (InventoryThresholdPage, error) {
	platform = strings.TrimSpace(strings.ToLower(platform))
	if platform == "" {
		return InventoryThresholdPage{}, errors.New("platform is required")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	values := url.Values{}
	values.Set("platform", platform)
	values.Set("page", fmt.Sprintf("%d", page))
	values.Set("page_size", fmt.Sprintf("%d", pageSize))
	if query = strings.TrimSpace(query); query != "" {
		values.Set("q", query)
	}
	var result InventoryThresholdPage
	if err := client.do(ctx, http.MethodGet, "/inventory-thresholds?"+values.Encode(), nil, "", "", "", &result); err != nil {
		return InventoryThresholdPage{}, err
	}
	return result, nil
}

func (client *Client) UpdatePlatformSKUInventoryThreshold(ctx context.Context, platform, warehouseSKU string, thresholds InventoryThresholds) (SKUInventoryThreshold, error) {
	platform = strings.TrimSpace(strings.ToLower(platform))
	if platform == "" {
		return SKUInventoryThreshold{}, errors.New("platform is required")
	}
	warehouseSKU = strings.TrimSpace(warehouseSKU)
	if warehouseSKU == "" {
		return SKUInventoryThreshold{}, errors.New("warehouse SKU is required")
	}
	body, err := json.Marshal(thresholds)
	if err != nil {
		return SKUInventoryThreshold{}, err
	}
	var result SKUInventoryThreshold
	if err := client.do(ctx, http.MethodPatch, "/inventory-thresholds/"+url.PathEscape(warehouseSKU)+"?platform="+url.QueryEscape(platform), body, "", "", "", &result); err != nil {
		return SKUInventoryThreshold{}, err
	}
	return result, nil
}

func (client *Client) CreateParcel(ctx context.Context, warehouse string, data any) (json.RawMessage, error) {
	return client.postOutbound(ctx, warehouse, "parcel-create", data)
}

func (client *Client) UpdateParcelTrackingLabel(ctx context.Context, warehouse string, data any) (json.RawMessage, error) {
	return client.postOutbound(ctx, warehouse, "tracking-label-update", data)
}

func (client *Client) LookupParcel(ctx context.Context, warehouse, thirdOrderNo, referOrderNo string) (json.RawMessage, error) {
	thirdOrderNo = strings.TrimSpace(thirdOrderNo)
	referOrderNo = strings.TrimSpace(referOrderNo)
	if thirdOrderNo == "" && referOrderNo == "" {
		return nil, errors.New("third order number is required")
	}
	if thirdOrderNo != "" {
		result, err := client.postOutbound(ctx, warehouse, "parcel-detail", map[string]any{
			"thirdOrderNoList": []any{thirdOrderNo},
		})
		if err != nil {
			return nil, err
		}
		if parcelDetailHasRecords(result) {
			return result, nil
		}
	}
	if referOrderNo == "" {
		referOrderNo = thirdOrderNo
	}
	if referOrderNo == "" {
		return json.RawMessage(`[]`), nil
	}
	return client.postOutbound(ctx, warehouse, "parcel-detail", map[string]any{
		"referOrderNoList": []any{referOrderNo},
	})
}

func (client *Client) CancelParcel(ctx context.Context, warehouse string, outboundOrderNos []string) (json.RawMessage, error) {
	values := outboundOrderValues(outboundOrderNos)
	if len(values) == 0 {
		return nil, errors.New("outbound order number is required")
	}
	return client.postOutbound(ctx, warehouse, "parcel-cancel", map[string]any{
		"outboundOrderNoList": values,
	})
}

func (client *Client) ParcelCancelStatus(ctx context.Context, warehouse string, outboundOrderNos []string) (json.RawMessage, error) {
	values := outboundOrderValues(outboundOrderNos)
	if len(values) == 0 {
		return nil, errors.New("outbound order number is required")
	}
	return client.postOutbound(ctx, warehouse, "cancel-status", map[string]any{
		"outboundOrderNoList": values,
	})
}

func (client *Client) LookupParcelByOutbound(ctx context.Context, warehouse string, outboundOrderNos []string) (json.RawMessage, error) {
	values := outboundOrderValues(outboundOrderNos)
	if len(values) == 0 {
		return nil, errors.New("outbound order number is required")
	}
	return client.postOutbound(ctx, warehouse, "parcel-detail", map[string]any{
		"outboundOrderNoList": values,
	})
}

func outboundOrderValues(outboundOrderNos []string) []any {
	values := make([]any, 0, len(outboundOrderNos))
	for _, outbound := range outboundOrderNos {
		if value := strings.TrimSpace(outbound); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func parcelDetailHasRecords(result json.RawMessage) bool {
	if len(result) == 0 {
		return false
	}
	var decoded any
	if err := json.Unmarshal(result, &decoded); err != nil {
		return false
	}
	return parcelDetailRecordCount(decoded) > 0
}

func parcelDetailRecordCount(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case map[string]any:
		for _, key := range []string{"data", "info", "records"} {
			if count := parcelDetailRecordCount(typed[key]); count > 0 {
				return count
			}
		}
	}
	return 0
}

func (client *Client) postOutbound(ctx context.Context, warehouse, operation string, data any) (json.RawMessage, error) {
	warehouse = strings.TrimSpace(warehouse)
	operation = strings.TrimSpace(operation)
	if warehouse == "" {
		return nil, errors.New("warehouse is required")
	}
	if operation == "" {
		return nil, errors.New("outbound operation is required")
	}
	body, err := json.Marshal(map[string]any{"warehouse": warehouse, "data": data})
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err := client.do(ctx, http.MethodPost, "/outbound/"+url.PathEscape(operation), body, "", "", "", &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (client *Client) ResetPlatformSKUInventoryThreshold(ctx context.Context, platform, warehouseSKU string) error {
	platform = strings.TrimSpace(strings.ToLower(platform))
	if platform == "" {
		return errors.New("platform is required")
	}
	warehouseSKU = strings.TrimSpace(warehouseSKU)
	if warehouseSKU == "" {
		return errors.New("warehouse SKU is required")
	}
	var result map[string]bool
	return client.do(ctx, http.MethodPost, "/inventory-thresholds/"+url.PathEscape(warehouseSKU)+"/reset?platform="+url.QueryEscape(platform), nil, "", "", "", &result)
}

func (client *Client) do(ctx context.Context, method, path string, body []byte, account, platform, shopCode string, target any) error {
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if account != "" {
		request.Header.Set("X-OMS-Account", account)
	}
	if platform == "temu" && shopCode != "" {
		request.Header.Set("X-Temu-Shop", shopCode)
	}
	if platform == "shein" && shopCode != "" {
		request.Header.Set("X-Shein-Shop", shopCode)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return &GatewayError{StatusCode: http.StatusBadGateway, Message: "无法连接领星服务"}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return &GatewayError{StatusCode: http.StatusBadGateway, Message: "无法读取领星服务响应"}
	}
	var gateway envelope
	if err := json.Unmarshal(raw, &gateway); err != nil {
		return &GatewayError{StatusCode: http.StatusBadGateway, Message: "领星服务返回了无效响应"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !gateway.Success {
		message := strings.TrimSpace(gateway.Error)
		if message == "" {
			message = fmt.Sprintf("领星服务返回 HTTP %d", response.StatusCode)
		}
		return &GatewayError{StatusCode: response.StatusCode, Message: message}
	}
	if target == nil || len(gateway.Data) == 0 || string(gateway.Data) == "null" {
		return nil
	}
	if rawTarget, ok := target.(*json.RawMessage); ok {
		*rawTarget = append((*rawTarget)[:0], gateway.Data...)
		return nil
	}
	if err := json.Unmarshal(gateway.Data, target); err != nil {
		return &GatewayError{StatusCode: http.StatusBadGateway, Message: "领星服务返回了无效数据"}
	}
	return nil
}
