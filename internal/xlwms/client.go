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

type ShopInventoryThresholds struct {
	Platform       string    `json:"platform"`
	ShopCode       string    `json:"shop_code"`
	ShopName       string    `json:"shop_name"`
	Enabled        bool      `json:"enabled"`
	EastThreshold  float64   `json:"east_threshold"`
	WestThreshold  float64   `json:"west_threshold"`
	TotalThreshold float64   `json:"total_threshold"`
	Customized     bool      `json:"customized"`
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
	Records           []SKUInventoryThreshold `json:"records"`
	Total             int                     `json:"total"`
	Page              int                     `json:"page"`
	PageSize          int                     `json:"page_size"`
	Pages             int                     `json:"pages"`
	DefaultThresholds ShopInventoryThresholds `json:"default_thresholds"`
}

func (client *Client) ShopInventoryThresholds(ctx context.Context, platform, shopCode string) (ShopInventoryThresholds, error) {
	var result ShopInventoryThresholds
	if err := client.do(ctx, http.MethodGet, "/inventory-thresholds/defaults?platform="+url.QueryEscape(platform)+"&shop="+url.QueryEscape(shopCode), nil, "", platform, shopCode, &result); err != nil {
		return ShopInventoryThresholds{}, err
	}
	return result, nil
}

func (client *Client) UpdateShopInventoryThresholds(ctx context.Context, platform, shopCode string, thresholds InventoryThresholds) (ShopInventoryThresholds, error) {
	body, err := json.Marshal(thresholds)
	if err != nil {
		return ShopInventoryThresholds{}, err
	}
	var result ShopInventoryThresholds
	if err := client.do(ctx, http.MethodPatch, "/inventory-thresholds/defaults?platform="+url.QueryEscape(platform)+"&shop="+url.QueryEscape(shopCode), body, "", platform, shopCode, &result); err != nil {
		return ShopInventoryThresholds{}, err
	}
	return result, nil
}

func (client *Client) ResetShopInventoryThresholds(ctx context.Context, platform, shopCode string) (ShopInventoryThresholds, error) {
	var result ShopInventoryThresholds
	if err := client.do(ctx, http.MethodPost, "/inventory-thresholds/defaults/reset?platform="+url.QueryEscape(platform)+"&shop="+url.QueryEscape(shopCode), nil, "", platform, shopCode, &result); err != nil {
		return ShopInventoryThresholds{}, err
	}
	return result, nil
}

func (client *Client) ListShopSKUInventoryThresholds(ctx context.Context, platform, shopCode, query string, page, pageSize int) (InventoryThresholdPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	values := url.Values{}
	values.Set("platform", platform)
	values.Set("shop", shopCode)
	values.Set("page", fmt.Sprintf("%d", page))
	values.Set("page_size", fmt.Sprintf("%d", pageSize))
	if query = strings.TrimSpace(query); query != "" {
		values.Set("q", query)
	}
	var result InventoryThresholdPage
	if err := client.do(ctx, http.MethodGet, "/inventory-thresholds?"+values.Encode(), nil, "", platform, shopCode, &result); err != nil {
		return InventoryThresholdPage{}, err
	}
	return result, nil
}

func (client *Client) UpdateShopSKUInventoryThreshold(ctx context.Context, platform, shopCode, warehouseSKU string, thresholds InventoryThresholds) (SKUInventoryThreshold, error) {
	warehouseSKU = strings.TrimSpace(warehouseSKU)
	if warehouseSKU == "" {
		return SKUInventoryThreshold{}, errors.New("warehouse SKU is required")
	}
	body, err := json.Marshal(thresholds)
	if err != nil {
		return SKUInventoryThreshold{}, err
	}
	var result SKUInventoryThreshold
	if err := client.do(ctx, http.MethodPatch, "/inventory-thresholds/"+url.PathEscape(warehouseSKU)+"?platform="+url.QueryEscape(platform)+"&shop="+url.QueryEscape(shopCode), body, "", platform, shopCode, &result); err != nil {
		return SKUInventoryThreshold{}, err
	}
	return result, nil
}

func (client *Client) ResetShopSKUInventoryThreshold(ctx context.Context, platform, shopCode, warehouseSKU string) error {
	warehouseSKU = strings.TrimSpace(warehouseSKU)
	if warehouseSKU == "" {
		return errors.New("warehouse SKU is required")
	}
	var result map[string]bool
	return client.do(ctx, http.MethodPost, "/inventory-thresholds/"+url.PathEscape(warehouseSKU)+"/reset?platform="+url.QueryEscape(platform)+"&shop="+url.QueryEscape(shopCode), nil, "", platform, shopCode, &result)
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
