package sheinconsole

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"shein-api-manager/internal/shein"
)

type fulfillmentCheckResult struct {
	PlaceRequestID       string
	DeliveryNo           string
	ExpressChannelCode   string
	WarehouseAddressCode string
	HandleResult         *int
	PrintStatus          *int
	Status               string
	FailureReason        string
}

func (s *Server) persistFulfillmentState(shopKey, operation string, requestData, result map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	recordError := func(err error) {
		if err != nil {
			s.logger.Warn("persist SHEIN fulfillment state failed", "operation", operation, "shop", shopKey, "error", sanitizedError(err))
		}
	}

	switch operation {
	case "order-detail":
		for _, task := range fulfillmentTasksFromOrderDetail(result) {
			recordError(s.store.UpsertFulfillmentTask(ctx, shopKey, task))
		}
	case "place-express-order":
		if task, ok := fulfillmentTaskFromPlace(requestData, result); ok {
			recordError(s.store.UpsertFulfillmentTask(ctx, shopKey, task))
		}
		info := firstObject(result["info"])
		recordError(s.store.UpdateLabelPurchaseResult(
			ctx, shopKey, firstString(requestData, "preRequestId"),
			firstString(info, "placeRequestId"), firstString(info, "deliveryNo")))
	case "check-express-order":
		check := fulfillmentResultFromCheck(requestData, result)
		recordError(s.store.UpdateFulfillmentTaskResult(
			ctx, shopKey, check.PlaceRequestID, check.DeliveryNo,
			check.ExpressChannelCode, check.WarehouseAddressCode,
			check.Status, check.FailureReason, check.HandleResult, check.PrintStatus,
		))
	case "print-express-info":
		deliveryNo, orderNo, packageNos := fulfillmentLabelIdentifiers(requestData)
		recordError(s.store.MarkFulfillmentTaskLabelReady(ctx, shopKey, deliveryNo, orderNo, packageNos))
	}
}

func (s *Server) saveShippingQuote(shopKey string, requestData, result map[string]any) error {
	quote, ok := shippingQuoteFromChannels(requestData, result)
	if !ok {
		if firstString(firstObject(result["info"]), "preRequestId") != "" {
			return nil
		}
		return errors.New("SHEIN channel response does not contain a complete quote")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.store.SaveShippingQuote(ctx, shopKey, quote)
}

func fulfillmentTasksFromOrderDetail(result map[string]any) []shein.FulfillmentTask {
	tasks := make([]shein.FulfillmentTask, 0)
	for _, detail := range objectItems(result["info"]) {
		orderNo := firstString(detail, "orderNo")
		orderPlaceType := integerPointer(detail["orderPlaceType"])
		for _, parcel := range objectItems(detail["packageWaybillList"]) {
			task := shein.FulfillmentTask{
				OrderNo:            firstNonEmpty(firstString(parcel, "orderNo"), orderNo),
				ExpressChannelCode: firstString(parcel, "expressChannelCode", "carrierCode", "expressIdCode"),
				PlaceRequestID:     firstString(parcel, "placeRequestId"),
				DeliveryNo:         firstString(parcel, "deliveryNo"),
				PackageNo:          firstString(parcel, "packageNo"),
				WaybillNo:          firstString(parcel, "waybillNo"),
				OrderPlaceType:     orderPlaceType,
				PrintStatus:        integerPointer(parcel["printOrderStatus"]),
				Status:             "discovered",
			}
			if firstString(parcel, "waybillNo") != "" {
				task.Status = "ready"
			}
			if nonEmptyValue(parcel["packageLabel"]) || task.PrintStatus != nil && *task.PrintStatus == 2 {
				task.Status = "label_ready"
			}
			if task.OrderNo != "" && (task.PlaceRequestID != "" || task.DeliveryNo != "" || task.PackageNo != "") {
				tasks = append(tasks, task)
			}
		}
	}
	return tasks
}

func fulfillmentTaskFromPlace(requestData, result map[string]any) (shein.FulfillmentTask, bool) {
	packages := objectItems(requestData["packageInfoList"])
	if len(packages) == 0 {
		return shein.FulfillmentTask{}, false
	}
	info := firstObject(result["info"])
	orderPlaceType := 2
	task := shein.FulfillmentTask{
		OrderNo:              firstString(packages[0], "orderNo"),
		ExpressChannelCode:   firstString(requestData, "expressChannelCode"),
		WarehouseAddressCode: firstString(info, "warehouseAddressCode"),
		PlaceRequestID:       firstString(info, "placeRequestId"),
		DeliveryNo:           firstString(info, "deliveryNo"),
		PackageNo:            firstNonEmpty(firstString(info, "packageNo"), firstString(info, "deliveryNo")),
		OrderPlaceType:       &orderPlaceType,
		Status:               "placed",
	}
	return task, task.OrderNo != "" && (task.PlaceRequestID != "" || task.DeliveryNo != "")
}

func fulfillmentResultFromCheck(requestData, result map[string]any) fulfillmentCheckResult {
	info := firstObject(result["info"])
	handleResult := integerPointer(info["handleResult"])
	printStatus := integerPointer(info["printStatus"])
	status := "checking"
	if handleResult != nil {
		switch *handleResult {
		case 1:
			status = "confirming"
		case 2:
			status = "ready"
		case 3:
			status = "failed"
		}
	}
	if printStatus != nil && *printStatus == 2 {
		status = "label_ready"
	}
	return fulfillmentCheckResult{
		PlaceRequestID:       firstNonEmpty(firstString(info, "placeRequestId"), firstString(requestData, "placeRequestId")),
		DeliveryNo:           firstNonEmpty(firstString(info, "deliveryNo"), firstString(requestData, "deliveryNo")),
		ExpressChannelCode:   firstString(info, "expressChannelCode"),
		WarehouseAddressCode: firstString(info, "warehouseAddressCode"),
		HandleResult:         handleResult,
		PrintStatus:          printStatus,
		Status:               status,
		FailureReason:        firstString(info, "placeStateFailReasonDesc", "declareSolutionDesc"),
	}
}

func fulfillmentLabelIdentifiers(data map[string]any) (string, string, []string) {
	return firstString(data, "deliveryNo"), firstString(data, "orderNo"), stringItems(data["packageNo"])
}

func firstObject(value any) map[string]any {
	items := objectItems(value)

	if len(items) == 0 {
		return map[string]any{}
	}
	return items[0]
}

func shippingQuoteFromChannels(requestData, result map[string]any) (shein.ShippingQuote, bool) {
	info := firstObject(result["info"])
	quote := shein.ShippingQuote{
		PreRequestID:         firstString(info, "preRequestId"),
		OrderNo:              firstNonEmpty(firstString(info, "orderNo"), firstString(requestData, "orderNo")),
		WarehouseAddressCode: firstNonEmpty(firstString(info, "warehouseAddressCode"), firstString(requestData, "warehouseAddressCode")),
	}
	for _, item := range objectItems(info["channelInfoList"]) {
		channelCode := firstString(item, "expressChannelCode")
		if channelCode == "" || channelPolicyBlocks(item) {
			continue
		}
		cost, _ := decimalValue(item["performanceCost"])
		quote.Candidates = append(quote.Candidates, shein.ShippingQuoteCandidate{
			ExpressChannelCode: channelCode,
			ExpressID:          integerPointer(item["expressId"]),
			ExpressIDCode:      firstString(item, "expressIdCode"),
			ExpressShortName:   firstString(item, "expressShortName"),
			PerformanceCost:    cost,
			CurrencyCode:       firstString(item, "currencyCode"),
			EstimateMinDay:     integerPointer(item["estimateMinDay"]),
			EstimateMaxDay:     integerPointer(item["estimateMaxDay"]),
		})
	}
	ok := quote.PreRequestID != "" && quote.OrderNo != "" &&
		quote.WarehouseAddressCode != "" && len(quote.Candidates) > 0
	return quote, ok
}

func channelPolicyBlocks(channel map[string]any) bool {
	reason := firstString(channel, "unavailableReason")
	if reason == "" {
		return false
	}
	status := firstString(channel, "availableStatus")
	return status == "0" || strings.Contains(reason, "已禁用") || strings.Contains(reason, "白名单")
}

func objectItems(value any) []map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return []map[string]any{typed}
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if object, ok := item.(map[string]any); ok {
				items = append(items, object)
			}
		}
		return items
	default:
		return nil
	}
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			switch typed := value.(type) {
			case string:
				if value := strings.TrimSpace(typed); value != "" {
					return value
				}
			case json.Number:
				if value := strings.TrimSpace(typed.String()); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func integerPointer(value any) *int {
	integer, ok := integerValue(value)
	if !ok {
		return nil
	}
	return &integer
}

func stringItems(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {

		if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
			values = append(values, strings.TrimSpace(value))
		}
	}
	return values

}
func decimalValue(value any) (string, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
			return "", false
		}
		return typed.String(), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed < 0 {
			return "", false
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case float32:
		parsed := float64(typed)
		if math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
			return "", false
		}
		return strconv.FormatFloat(parsed, 'f', -1, 32), true
	case int:
		if typed < 0 {
			return "", false
		}
		return strconv.Itoa(typed), true
	default:
		return "", false
	}
}

func nonEmptyValue(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return value != nil
	}
}
