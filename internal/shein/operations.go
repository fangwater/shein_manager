package shein

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	OrderListPath                  = "/open-api/order/order-list"
	OrderDetailPath                = "/open-api/order/order-detail"
	ExportAddressPath              = "/open-api/order/export-address"
	AvailableShippingWarehousePath = "/open-api/gsp/available-shipping-warehouse"
	OrderMappingChannelsPath       = "/open-api/gsp/order-mapping-channels"
	PlaceExpressOrderPath          = "/open-api/gsp/place-express-order"
	CheckExpressOrderPath          = "/open-api/gsp/check-express-order"
	PrintExpressInfoPath           = "/open-api/order/print-express-info"
	LogisticsTrackPath             = "/open-api/gsp/logistics-track"
	SHEINTimeFormat                = "2006-01-02 15:04:05"
	MaxOrderListWindow             = 48 * time.Hour
	MaxOrderListPageSize           = 30
	MaxOrderDetailBatch            = 30
)

type Endpoint struct {
	Method string
	Path   string
}

var Endpoints = map[string]Endpoint{
	"order-list":                   {Method: http.MethodPost, Path: OrderListPath},
	"order-detail":                 {Method: http.MethodPost, Path: OrderDetailPath},
	"export-address":               {Method: http.MethodPost, Path: ExportAddressPath},
	"available-shipping-warehouse": {Method: http.MethodPost, Path: AvailableShippingWarehousePath},
	"order-mapping-channels":       {Method: http.MethodPost, Path: OrderMappingChannelsPath},
	"place-express-order":          {Method: http.MethodPost, Path: PlaceExpressOrderPath},
	"check-express-order":          {Method: http.MethodPost, Path: CheckExpressOrderPath},
	"print-express-info":           {Method: http.MethodPost, Path: PrintExpressInfoPath},
	"logistics-track":              {Method: http.MethodGet, Path: LogisticsTrackPath},
}

func (c *Client) Call(ctx context.Context, operation string, data map[string]any) (map[string]any, error) {
	endpoint, ok := Endpoints[operation]
	if !ok {
		return nil, errors.New("unknown SHEIN operation")
	}
	if endpoint.Method == http.MethodGet {
		return nil, errors.New("use LogisticsTrack for the GET operation")
	}
	if err := Validate(operation, data); err != nil {
		return nil, err
	}
	result, err := c.Request(ctx, endpoint.Method, endpoint.Path, outboundRequestData(operation, data), nil)
	if err != nil {
		return nil, err
	}
	if operation == "available-shipping-warehouse" {
		RestrictShippingWarehouseAvailability(result)
	}
	return result, nil
}

func (c *Client) LogisticsTrack(ctx context.Context, orderNo, packageNo, waybillNo, returnOrderNo string) (map[string]any, error) {
	orderNo = strings.TrimSpace(orderNo)
	packageNo = strings.TrimSpace(packageNo)
	waybillNo = strings.TrimSpace(waybillNo)
	returnOrderNo = strings.TrimSpace(returnOrderNo)
	if returnOrderNo == "" && (orderNo == "" || packageNo == "" && waybillNo == "") {
		return nil, errors.New("returnOrderNo or orderNo with packageNo/waybillNo is required")
	}
	if returnOrderNo != "" && (orderNo != "" || packageNo != "" || waybillNo != "") {
		return nil, errors.New("returnOrderNo cannot be combined with forward-order tracking fields")
	}
	query := url.Values{}
	if orderNo != "" {
		query.Set("orderNo", orderNo)
	}
	if packageNo != "" {
		query.Set("packageNo", packageNo)
	}
	if waybillNo != "" {
		query.Set("waybillNo", waybillNo)
	}
	if returnOrderNo != "" {
		query.Set("returnOrderNo", returnOrderNo)
	}
	return c.Request(ctx, http.MethodGet, LogisticsTrackPath, nil, query)
}

func outboundRequestData(operation string, data map[string]any) map[string]any {
	if operation != "order-mapping-channels" || data == nil {
		return data
	}
	if _, ok := data["warehouseName"]; !ok {
		return data
	}
	outbound := make(map[string]any, len(data))
	for key, value := range data {
		if key == "warehouseName" {
			continue
		}
		outbound[key] = value
	}
	return outbound
}

func Validate(operation string, data map[string]any) error {
	if data == nil {
		return errors.New("data is required")
	}
	switch operation {
	case "order-list":
		return validateOrderList(data)
	case "order-detail":
		return validateStringList(data, "orderNoList", 1, MaxOrderDetailBatch)
	case "export-address":
		if err := requireString(data, "orderNo"); err != nil {
			return err
		}
		handleType, ok := integer(data["handleType"])
		if !ok || handleType != 1 && handleType != 2 {
			return errors.New("handleType is required and must be 1 or 2")
		}
	case "available-shipping-warehouse":
		return requireString(data, "orderNo")
	case "order-mapping-channels":
		return validateMappingChannels(data)
	case "place-express-order":
		return validatePlaceExpressOrder(data)
	case "check-express-order":
		if !hasString(data, "placeRequestId", "deliveryNo") {
			return errors.New("placeRequestId or deliveryNo is required")
		}
	case "print-express-info":
		if hasString(data, "deliveryNo") {
			return nil
		}
		if err := requireString(data, "orderNo"); err != nil {
			return errors.New("deliveryNo or orderNo with packageNo is required")
		}
		if err := validateStringList(data, "packageNo", 1, 100); err != nil {
			return errors.New("packageNo must contain between 1 and 100 values")
		}
	case "logistics-track":
		return errors.New("logistics-track uses query parameters")
	default:
		return errors.New("unknown SHEIN operation")
	}
	return nil
}

func validateOrderList(data map[string]any) error {
	queryType, ok := integer(data["queryType"])
	if !ok || queryType != 1 && queryType != 2 {
		return errors.New("queryType is required and must be 1 or 2")
	}
	startRaw, startOK := stringValue(data["startTime"])
	endRaw, endOK := stringValue(data["endTime"])
	if !startOK || !endOK {
		return errors.New("startTime and endTime are required")
	}
	start, err := time.ParseInLocation(SHEINTimeFormat, startRaw, time.FixedZone("UTC+8", 8*60*60))
	if err != nil {
		return errors.New("startTime must use yyyy-MM-dd HH:mm:ss")
	}
	end, err := time.ParseInLocation(SHEINTimeFormat, endRaw, time.FixedZone("UTC+8", 8*60*60))
	if err != nil {
		return errors.New("endTime must use yyyy-MM-dd HH:mm:ss")
	}
	if end.Before(start) || end.Sub(start) > MaxOrderListWindow {
		return errors.New("order list time window must be positive and no more than 48 hours")
	}
	page, ok := integer(data["page"])
	if !ok || page < 1 {
		return errors.New("page is required and must be positive")
	}
	pageSize, ok := integer(data["pageSize"])
	if !ok || pageSize < 1 || pageSize > MaxOrderListPageSize {
		return fmt.Errorf("pageSize is required and must be between 1 and %d", MaxOrderListPageSize)
	}
	return nil
}

func validateMappingChannels(data map[string]any) error {
	for _, field := range []string{"orderNo", "warehouseAddressCode"} {
		if err := requireString(data, field); err != nil {
			return err
		}
	}
	code, _ := stringValue(data["warehouseAddressCode"])
	name, _ := stringValue(data["warehouseName"])
	if !IsAllowedShippingWarehouse(code, name) {
		if IsPGWarehouse(code, name) {
			return errors.New(pgWarehouseUnavailableReason)
		}
		return errors.New(unoperatedWarehouseUnavailableReason)
	}
	size, ok := object(data["packageSizeInfo"])
	if !ok {
		return errors.New("packageSizeInfo is required")
	}
	for _, field := range []string{"packageHeight", "packageLength", "packageWidth"} {
		if err := requirePositiveNumberString(size, field); err != nil {
			return err
		}
	}
	if unit, ok := stringValue(size["unit"]); !ok || !strings.EqualFold(unit, "cm") {
		return errors.New("packageSizeInfo.unit must be cm")
	}
	weight, ok := object(data["packageWeightInfo"])
	if !ok {
		return errors.New("packageWeightInfo is required")
	}
	if err := requirePositiveNumberString(weight, "packageWeight"); err != nil {
		return err
	}
	if unit, ok := stringValue(weight["unit"]); !ok || !strings.EqualFold(unit, "g") {
		return errors.New("packageWeightInfo.unit must be g")
	}
	return nil
}

func validatePlaceExpressOrder(data map[string]any) error {
	for _, field := range []string{"expressChannelCode", "preRequestId"} {
		if err := requireString(data, field); err != nil {
			return err
		}
	}
	packages, ok := array(data["packageInfoList"])
	if !ok || len(packages) == 0 {
		return errors.New("packageInfoList must contain at least one package")
	}
	for index, item := range packages {
		entry, ok := object(item)
		if !ok {
			return fmt.Errorf("packageInfoList[%d] must be an object", index)
		}
		if err := requireString(entry, "orderNo"); err != nil {
			return fmt.Errorf("packageInfoList[%d].orderNo is required", index)
		}
	}
	return nil
}

func validateStringList(data map[string]any, field string, minimum, maximum int) error {
	items, ok := array(data[field])
	if !ok || len(items) < minimum || len(items) > maximum {
		return fmt.Errorf("%s must contain between %d and %d values", field, minimum, maximum)
	}
	for _, item := range items {
		if value, ok := stringValue(item); !ok || value == "" {
			return fmt.Errorf("%s must contain only non-empty strings", field)
		}
	}
	return nil
}

func requireString(data map[string]any, field string) error {
	if value, ok := stringValue(data[field]); !ok || value == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

func hasString(data map[string]any, fields ...string) bool {
	for _, field := range fields {
		if value, ok := stringValue(data[field]); ok && value != "" {
			return true
		}
	}
	return false
}

func requirePositiveNumberString(data map[string]any, field string) error {
	value, ok := stringValue(data[field])
	if !ok {
		return fmt.Errorf("%s is required", field)
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("%s must be a positive number", field)
	}
	return nil
}

func stringValue(value any) (string, bool) {
	text, ok := value.(string)
	return strings.TrimSpace(text), ok
}

func integer(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		parsed, err := strconv.Atoi(string(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func object(value any) (map[string]any, bool) {
	result, ok := value.(map[string]any)
	return result, ok
}

func array(value any) ([]any, bool) {
	result, ok := value.([]any)
	return result, ok
}
