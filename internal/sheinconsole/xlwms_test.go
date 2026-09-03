package sheinconsole

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shein-api-manager/internal/shein"
	"shein-api-manager/internal/xlwms"
)

func TestWarehouseQuantitiesAggregatesMappedGoods(t *testing.T) {
	quantities, missing := warehouseQuantities(shein.OrderQueueItem{Goods: []shein.QueueGoods{
		{WarehouseSKU: "WH-A", WarehouseQuantity: "1.2"},
		{WarehouseSKU: "WH-A", WarehouseQuantity: "2"},
		{SKUCode: "SHEIN-B"},
		{SKUCode: "SHEIN-B"},
	}})
	if quantities["WH-A"] != 4 {
		t.Fatalf("WH-A quantity = %d, want 4", quantities["WH-A"])
	}
	if len(missing) != 1 || missing[0] != "SHEIN-B" {
		t.Fatalf("missing = %#v", missing)
	}
}

func TestXLWMSPlatformOrderQueriesEveryAccountWithoutLogin(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/platform-orders/accounts" {
			_, _ = writer.Write([]byte(`{"success":true,"data":[{"key":"dps","label":"DPS"},{"key":"arp","label":"ARP"}]}`))
			return
		}
		if request.URL.Path != "/temu/platform-orders/SHEIN-ORDER-1" {
			http.NotFound(writer, request)
			return
		}
		account := request.Header.Get("X-OMS-Account")
		found := account == "arp"
		orders := "[]"
		count := 0
		if found {
			orders = `[{"oms_order_no":"OMS-1","platform_order_no":"SHEIN-ORDER-1","platform_code":"SHEIN","status":2,"status_key":"processing"}]`
			count = 1
		}
		_, _ = writer.Write([]byte(`{"success":true,"data":{"account":"` + account + `","platform_order_no":"SHEIN-ORDER-1","found":` + boolText(found) + `,"match_count":` + numberText(count) + `,"orders":` + orders + `,"queried_at":"2026-08-14T00:00:00Z"}}`))
	}))
	defer gateway.Close()
	client, err := xlwms.NewClient(gateway.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{xlwms: client, requestTimeout: time.Second}).routes()
	request := httptest.NewRequest(http.MethodGet, "/api/oms-platform-orders/SHEIN-ORDER-1?account=all", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Success bool              `json:"success"`
		Data    xlwmsLookupResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Success || !payload.Data.Found || payload.Data.MatchCount != 1 || len(payload.Data.Accounts) != 2 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestInventoryThresholdRoutesUsePlatformScope(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/inventory-thresholds" {
			http.NotFound(writer, request)
			return
		}
		if request.URL.Query().Get("platform") != "shein" || request.URL.Query().Has("shop") {
			t.Fatalf("query = %s", request.URL.RawQuery)
		}
		if request.Header.Get("X-Shein-Shop") != "" {
			t.Fatalf("unexpected X-Shein-Shop = %q", request.Header.Get("X-Shein-Shop"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":{"records":[{"warehouse_sku":"WH-1","east_threshold":1,"west_threshold":2,"total_threshold":3,"customized":false,"source":"platform_default"}],"page":1,"page_size":30,"total":1,"pages":1,"default_thresholds":{"platform":"shein","east_threshold":10,"west_threshold":20,"total_threshold":30}}}`))
	}))
	defer gateway.Close()
	client, err := xlwms.NewClient(gateway.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{xlwms: client, shopKey: "beauty-hangers-home", requestTimeout: time.Second}).routes()
	request := httptest.NewRequest(http.MethodGet, "/api/inventory-thresholds?page=1&page_size=30", nil)
	request.Header.Set(shopHeader, "beauty-hangers-home")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Success bool `json:"success"`
		Data    []struct {
			WarehouseSKU string `json:"warehouse_sku"`
		} `json:"data"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Success || len(payload.Data) != 1 || payload.Data[0].WarehouseSKU != "WH-1" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Meta["total"] != float64(1) {
		t.Fatalf("meta = %#v", payload.Meta)
	}
}

func TestInventoryThresholdDefaultResetIsGone(t *testing.T) {
	recorder := httptest.NewRecorder()
	inventoryThresholdDefaultResetGone(recorder, httptest.NewRequest(http.MethodPost, "/api/inventory-thresholds/defaults/reset", nil))
	if recorder.Code != http.StatusGone {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestLingxingParcelCompletesManualCreateWhenLabeled(t *testing.T) {
	processing := 2
	draft := 0
	canceled := 4
	if !lingxingParcelCompletesManualCreate(lingxingParcelStatus{
		OutboundOrderNo: "OB-1", Status: &processing, LabelAttached: true,
	}) {
		t.Fatal("labeled warehouse-processing parcels must leave the complementary-upload queue")
	}
	if !lingxingParcelCompletesManualCreate(lingxingParcelStatus{
		OutboundOrderNo: "OB-1", Status: &draft, LabelAttached: true,
	}) {
		t.Fatal("labeled draft parcels must leave the complementary-upload queue")
	}
	if lingxingParcelCompletesManualCreate(lingxingParcelStatus{
		OutboundOrderNo: "OB-1", Status: &processing, LabelAttached: false,
	}) {
		t.Fatal("unlabeled parcels must stay in the complementary-upload queue")
	}
	if lingxingParcelCompletesManualCreate(lingxingParcelStatus{
		OutboundOrderNo: "OB-1", Status: &canceled, LabelAttached: true,
	}) {
		t.Fatal("canceled parcels must stay in the complementary-upload queue")
	}
}

func TestLingxingParcelAllowsLabelUpdateOnlyWhileProcessing(t *testing.T) {
	processing := 2
	draftStatus := 0
	labelException := 7
	if !lingxingParcelAllowsLabelUpdate(lingxingParcelStatus{
		OutboundOrderNo: "OB-1", Status: &processing, Channel: lingxingPlatformLabelChannel,
	}) {
		t.Fatal("warehouse-processing custom-channel parcels must allow label upload")
	}
	if !lingxingParcelAllowsLabelUpdate(lingxingParcelStatus{
		OutboundOrderNo: "OB-1", Status: &labelException, Channel: lingxingPlatformLabelChannel,
	}) {
		t.Fatal("label-exception parcels must allow complementary label upload")
	}
	if lingxingParcelAllowsLabelUpdate(lingxingParcelStatus{
		OutboundOrderNo: "OB-1", Status: &draftStatus, Channel: lingxingPlatformLabelChannel,
	}) {
		t.Fatal("draft parcels must not allow complementary label upload")
	}
}

func TestApplyLingxingParcelToDraftExplainsDraftUpload(t *testing.T) {
	draft := xlwmsParcelDraft{Required: true, OrderNo: "GSU-1"}
	status := 0
	applyLingxingParcelToDraft(&draft, lingxingParcelStatus{
		OutboundOrderNo: "OB-1", Status: &status, StatusName: "草稿", Channel: lingxingPlatformLabelChannel,
	})
	if draft.CanUploadLabel || draft.OutboundOrderNo != "OB-1" || !strings.Contains(draft.UploadHint, "还是草稿") {
		t.Fatalf("draft = %#v", draft)
	}
}

func TestParcelCancelStatusHelpers(t *testing.T) {
	if !parcelCancelStatusSucceeded(json.RawMessage(`{"data":[{"outboundOrderNo":"OB-1","status":1,"msg":"自动拦截处理成功"}]}`), "OB-1") {
		t.Fatal("successful cancel status was not recognized")
	}
	if message := parcelCancelStatusFailure(json.RawMessage(`[{"outboundOrderNo":"OB-1","status":2,"msg":"仓库已开始处理"}]`), "OB-1"); message != "仓库已开始处理" && !strings.Contains(message, "取消失败") {
		t.Fatalf("failed cancel = %q", message)
	}
	if parcelCancelStatusSucceeded(json.RawMessage(`[{"outboundOrderNo":"OB-1","status":0}]`), "OB-1") {
		t.Fatal("in-progress cancel was treated as success")
	}
}

func TestFirstActiveLingxingParcelSkipsCanceledOrders(t *testing.T) {
	parcel := firstActiveLingxingParcel([]any{
		map[string]any{"outboundOrderNo": "OB-OLD", "status": float64(4)},
		map[string]any{"outboundOrderNo": "OB-NEW", "status": float64(0), "statusName": "草稿"},
	})
	if parcel.OutboundOrderNo != "OB-NEW" || parcel.Status == nil || *parcel.Status != 0 {
		t.Fatalf("parcel = %#v", parcel)
	}
}

func TestParcelCancelFailureReadsRejectedFlag(t *testing.T) {
	if message := parcelCancelFailure(json.RawMessage(`false`)); message == "" {
		t.Fatal("false cancel result was treated as success")
	}
	if message := parcelCancelFailure(json.RawMessage(`{"success":false,"msg":"出库单已出库"}`)); message != "出库单已出库" {
		t.Fatalf("object cancel = %q", message)
	}
}

func TestReceiverNameFromAddressJoinsGivenAndFamilyName(t *testing.T) {
	if got := receiverNameFromAddress(map[string]any{
		"firstName": "JHOANA", "lastName": "ORTEGA", "middleName": nil,
	}); got != "JHOANA ORTEGA" {
		t.Fatalf("joined name = %q", got)
	}
	if got := receiverNameFromAddress(map[string]any{"receiver": "Ada Lovelace"}); got != "Ada Lovelace" {
		t.Fatalf("fallback name = %q", got)
	}
	draft := xlwmsParcelDraft{Receiver: "JHOANA"}
	applyAddressToParcelDraft(&draft, map[string]any{"firstName": "JHOANA", "lastName": "ORTEGA"})
	if draft.Receiver != "JHOANA ORTEGA" {
		t.Fatalf("address draft receiver = %q", draft.Receiver)
	}
}

func TestParcelDraftFromTaskRequiresBeautyHangersDPSOnly(t *testing.T) {
	dps := shein.FulfillmentTask{
		OrderNo: "GSU-DPS-1", WarehouseAddressCode: "WH2604283535967233",
		ExpressChannelCode: "USPS", WaybillNo: "1Z999",
	}
	draft := parcelDraftFromTask("beauty-hangers-home", "Beauty Hangers home", dps)
	if !draft.Required || draft.Warehouse != "DPSNY002" || draft.ChannelCode != lingxingPlatformLabelChannel ||
		draft.ChannelHint != "USPS" || draft.TrackingNumber != "1Z999" ||
		draft.SalesPlatform != "SHEIN" || draft.StoreName != "BeautyHanger" {
		t.Fatalf("DPS draft = %#v", draft)
	}
	arp := parcelDraftFromTask("beauty-hangers-home", "Beauty Hangers home", shein.FulfillmentTask{
		OrderNo: "GSU-ARP-1", WarehouseAddressCode: "WH2607084039788546",
	})
	if arp.Required || arp.Warehouse != "" {
		t.Fatalf("ARP draft = %#v", arp)
	}
	otherShop := parcelDraftFromTask("other-shop", "Other Shop", dps)
	if otherShop.Required {
		t.Fatalf("unconfigured shop inherited the DPS rule: %#v", otherShop)
	}
	delivered := parcelDraftFromTask("beauty-hangers-home", "Beauty Hangers home", shein.FulfillmentTask{
		OrderNo: "GSU1RW64S000VTQ", WarehouseAddressCode: "WH2603303477748739",
		OrderStatus: "5", OrderStatusNormalized: "delivered",
	})
	if delivered.Required || delivered.Ready {
		t.Fatalf("delivered SHEIN orders must leave complementary upload: %#v", delivered)
	}
	processing := 2
	warehouseProcessing := parcelDraftFromTask("beauty-hangers-home", "Beauty Hangers home", shein.FulfillmentTask{
		OrderNo: "GSU1RY59300M3TG", WarehouseAddressCode: "WH2604283535967233",
		OutboundOrderNo: "OBS5272608180XC", OutboundStatus: &processing, LabelAttached: true, ParcelComplete: true,
		OrderStatus: "7", OrderStatusNormalized: "pending_pickup",
	})
	if warehouseProcessing.Required || warehouseProcessing.Ready {
		t.Fatalf("labeled warehouse-processing parcels must leave complementary upload: %#v", warehouseProcessing)
	}
	labelException := 7
	exceptionDraft := parcelDraftFromTask("beauty-hangers-home", "Beauty Hangers home", shein.FulfillmentTask{
		OrderNo: "GSU-EXCEPTION", WarehouseAddressCode: "WH2604283535967233",
		OutboundOrderNo: "OBS-7", OutboundStatus: &labelException, LabelAttached: true, ParcelComplete: false,
	})
	if !exceptionDraft.Required {
		t.Fatalf("label-exception parcels must stay complementary: %#v", exceptionDraft)
	}
}

func TestFinalizeParcelDraftMarksMissingFields(t *testing.T) {
	draft := xlwmsParcelDraft{Required: true, Warehouse: "DPSNY002", ChannelCode: lingxingPlatformLabelChannel, SalesPlatform: "SHEIN", StoreName: "Beauty Hangers home"}
	finalizeParcelDraft(&draft)
	if draft.Ready || len(draft.MissingFields) == 0 {
		t.Fatalf("incomplete draft was marked ready: %#v", draft)
	}
	draft = xlwmsParcelDraft{
		Required: true, Warehouse: "DPSNY002", ChannelCode: lingxingPlatformLabelChannel, SalesPlatform: "SHEIN",
		StoreName: "Beauty Hangers home", Receiver: "Ada",
		CountryRegionCode: "US", ProvinceName: "CA", CityName: "LA", PostCode: "90001",
		AddressOne: "1 Main", Products: []xlwmsParcelDraftProduct{{SKU: "WH-1", Quantity: 1}},
	}
	finalizeParcelDraft(&draft)
	if !draft.Ready || draft.ProvinceCode != "CA" || len(draft.MissingFields) != 0 {
		t.Fatalf("complete draft = %#v", draft)
	}
}

func TestMergeParcelDraftProductsAggregatesSKULines(t *testing.T) {
	merged := mergeParcelDraftProducts([]xlwmsParcelDraftProduct{
		{SKU: "WH-A", Quantity: 1},
		{SKU: "WH-A", Quantity: 2},
		{SKU: "", Quantity: 3},
		{SKU: "WH-B", Quantity: 0},
		{SKU: "WH-B", Quantity: 4},
	})
	if len(merged) != 2 || merged[0] != (xlwmsParcelDraftProduct{SKU: "WH-A", Quantity: 3}) || merged[1].SKU != "WH-B" || merged[1].Quantity != 4 {
		t.Fatalf("merged = %#v", merged)
	}
}

func TestLingxingParcelChannelUsesUploadLabelForSheinCodes(t *testing.T) {
	if got := lingxingParcelChannel("GOFO-D2D250718-Na", "GOFO-D2D250718-Na"); got != lingxingPlatformLabelChannel {
		t.Fatalf("shein channel = %q", got)
	}
	if got := lingxingParcelChannel("", "US-AWr-PU-P-LAX-Na"); got != lingxingPlatformLabelChannel {
		t.Fatalf("shein suffix = %q", got)
	}
	if got := lingxingParcelChannel("GOFO-D2D250718-Na", ""); got != lingxingPlatformLabelChannel {
		t.Fatalf("empty channel = %q", got)
	}
	if got := lingxingParcelChannel("GOFO-D2D250718-Na", "CUSTOM-DPS"); got != "CUSTOM-DPS" {
		t.Fatalf("override channel = %q", got)
	}
}

func TestFirstSheinLabelURLPrefersFilePdfUrl(t *testing.T) {
	url := firstSheinLabelURL(map[string]any{"info": []any{map[string]any{
		"deliveryNo":   "GU-1",
		"filePdfUrl":   "https://pdf.sheinbackend.com/label.pdf",
		"packageLabel": "",
	}}})
	if url != "https://pdf.sheinbackend.com/label.pdf" {
		t.Fatalf("url = %q", url)
	}
	if got := firstSheinLabelURL(map[string]any{"info": map[string]any{"fileUrl": "http://insecure.example/label.pdf"}}); got != "" {
		t.Fatalf("insecure url = %q", got)
	}
}

func TestFirstLingxingOutboundOrderNoReadsCreateResult(t *testing.T) {
	outbound := firstLingxingOutboundOrderNo(json.RawMessage(`{"code":200,"data":[{"success":true,"outboundOrderNo":"OB-1","thirdOrderNo":"GSU-1"}]}`))
	if outbound != "OB-1" {
		t.Fatalf("outbound = %q", outbound)
	}
	if got := firstLingxingOutboundOrderNo(json.RawMessage(`{"data":[{"success":false,"orderNo":"OB-BAD"}]}`)); got != "" {
		t.Fatalf("failed line = %q", got)
	}
	if got := firstLingxingOutboundOrderNo(json.RawMessage(`[{"success":true,"orderNo":"OB-2"}]`)); got != "OB-2" {
		t.Fatalf("array orderNo = %q", got)
	}
}

func TestPrintExpressRequestPrefersDeliveryNo(t *testing.T) {
	data, err := printExpressRequest(shein.FulfillmentTask{DeliveryNo: "GU-1", OrderNo: "GSU-1", PackageNo: "PKG-1"})
	if err != nil || data["deliveryNo"] != "GU-1" {
		t.Fatalf("platform label request = %#v err=%v", data, err)
	}
	placeType := 1
	data, err = printExpressRequest(shein.FulfillmentTask{
		OrderPlaceType: &placeType, OrderNo: "GSU-1", PackageNo: "PKG-1", DeliveryNo: "GU-1",
	})
	if err != nil || data["orderNo"] != "GSU-1" {
		t.Fatalf("self-print request = %#v err=%v", data, err)
	}
}

func TestIsLingxingLabelUpdateUnavailable(t *testing.T) {
	if !isLingxingLabelUpdateUnavailable(errors.New("仅状态=仓库处理中，且渠道类型=自定义渠道的一件代发出库单支持更新面单")) {
		t.Fatal("status restriction was not recognized")
	}
	if isLingxingLabelUpdateUnavailable(errors.New("创建出库单失败")) {
		t.Fatal("generic failure was treated as an update restriction")
	}
}

func TestParcelLabelUploadFailureReadsRejectedFlag(t *testing.T) {
	if message := parcelLabelUploadFailure(json.RawMessage(`false`)); message != "领星面单上传失败" {
		t.Fatalf("false result = %q", message)
	}
	if message := parcelLabelUploadFailure(json.RawMessage(`{"success":false,"msg":"面单无效"}`)); message != "面单无效" {
		t.Fatalf("object result = %q", message)
	}
	if message := parcelLabelUploadFailure(json.RawMessage(`true`)); message != "" {
		t.Fatalf("success result = %q", message)
	}
}

func TestParcelCreateFailureReadsLingxingLineError(t *testing.T) {
	message := parcelCreateFailure(json.RawMessage(`{"code":200,"msg":"操作成功","data":[{"success":false,"msg":"您无法使用物流渠道【GOFO-D2D250718-Na】，请联系仓库给您启用该渠道","thirdOrderNo":"GSU-1"}]}`))
	if !strings.Contains(message, "您无法使用物流渠道") {
		t.Fatalf("message = %q", message)
	}
	if message := parcelCreateFailure(json.RawMessage(`{"code":200,"data":[{"success":true,"orderNo":"OB-1"}]}`)); message != "" {
		t.Fatalf("success message = %q", message)
	}
	if message := parcelCreateFailure(json.RawMessage(`{"code":200,"msg":"创建出库单失败","data":[{"success":false}]}`)); message != "创建出库单失败" {
		t.Fatalf("generic failure = %q", message)
	}
}

func TestParcelCreateOrderUsesResolvedDPSWarehouse(t *testing.T) {
	order, err := parcelCreateOrder("beauty-hangers-home", "Beauty Hangers home", "GSU-1", shein.FulfillmentTask{
		WarehouseAddressCode: "WH2603303477748739", ExpressChannelCode: "GOFO-D2D250718-Na",
	}, xlwmsParcelCreateRequest{
		Warehouse: "DPSCA004", ThirdOrderNo: "GSU-1", ChannelCode: "GOFO-D2D250718-Na",
		Receiver: "Ada", CountryRegionCode: "us", ProvinceCode: "CA", ProvinceName: "California",
		CityName: "Los Angeles", PostCode: "90001", AddressOne: "1 Main St", Phone: "555-0100",
		TrackingNumber: "1Z999", Products: []xlwmsParcelDraftProduct{{SKU: "WH-1", Quantity: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if order["whCode"] != "DPSCA004" || order["platformOrderNo"] != "GSU-1" || order["thirdOrderNo"] != "GSU-1" ||
		order["salesPlatform"] != "SHEIN" || order["store"] != "BeautyHanger" || order["storeName"] != "BeautyHanger" ||
		order["subOrderType"] != 1 || order["logisticsChannel"] != lingxingPlatformLabelChannel {
		t.Fatalf("order = %#v", order)
	}
	if _, ok := order["referOrderNo"]; ok {
		t.Fatalf("SHEIN platform order number was sent as referOrderNo: %#v", order)
	}
	if order["countryRegionCode"] != "US" || order["logisticsTrackNo"] != "1Z999" ||
		order["telephone"] != "555-0100" || order["phone"] != "555-0100" {
		t.Fatalf("optional fields = %#v", order)
	}
	attachLingxingLabelFiles(order, "GSU-1", "https://pdf.sheinbackend.com/label.pdf", []byte("%PDF-1.4 test"))
	files, ok := order["fileList"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("fileList = %#v", order["fileList"])
	}
	file, ok := files[0].(map[string]any)
	if !ok || file["fileName"] != "GSU-1.pdf" || file["fileType"] != "pdf" || file["bizCode"] != "1" || file["fileData"] == "" {
		t.Fatalf("label file = %#v", file)
	}
	if _, hasURL := file["fileUrl"]; hasURL {
		t.Fatalf("create fileList must use fileData, not fileUrl: %#v", file)
	}
	label, ok := order["label"].(map[string]any)
	if !ok || label["fileType"] != "pdf" || label["fileData"] == "" {
		t.Fatalf("label = %#v", order["label"])
	}
	if order["labelUrl"] != "https://pdf.sheinbackend.com/label.pdf" {
		t.Fatalf("labelUrl = %#v", order["labelUrl"])
	}
	if _, err := parcelCreateOrder("beauty-hangers-home", "Beauty Hangers home", "GSU-1", shein.FulfillmentTask{
		WarehouseAddressCode: "WH2603303477748739",
	}, xlwmsParcelCreateRequest{
		Warehouse: "DPSNY002", ChannelCode: "USPS", Receiver: "Ada", CountryRegionCode: "US",
		ProvinceName: "CA", CityName: "LA", PostCode: "90001", AddressOne: "1 Main",
		Products: []xlwmsParcelDraftProduct{{SKU: "WH-1", Quantity: 1}},
	}); err == nil || err.Error() != "建单仓库必须与 DPS 发货仓一致" {
		t.Fatalf("warehouse mismatch error = %v", err)
	}
	if _, err := parcelCreateOrder("other-shop", "", "GSU-1", shein.FulfillmentTask{
		WarehouseAddressCode: "WH2603303477748739",
	}, xlwmsParcelCreateRequest{
		Warehouse: "DPSCA004", ChannelCode: "Upload_Shipping_Label", Receiver: "Ada",
		CountryRegionCode: "US", ProvinceName: "CA", CityName: "LA", PostCode: "90001",
		AddressOne: "1 Main", Products: []xlwmsParcelDraftProduct{{SKU: "WH-1", Quantity: 1}},
	}); err == nil || err.Error() != "销售平台和店铺不能为空" {
		t.Fatalf("missing store error = %v", err)
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func numberText(value int) string {
	if value == 1 {
		return "1"
	}
	return "0"
}
