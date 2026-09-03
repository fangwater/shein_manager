package sheinconsole

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConsoleUsesSharedTemuShell(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Server{}).index(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("index status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, required := range []string{
		`href="/temu/dashboard.css`,
		`id="view-oms-statuses"`,
		`data-oms-status="0"`,
		`data-oms-status="1"`,
		`data-oms-status="2"`,
		"按领星平台订单状态查看自动发货核验与归档结果",
		`id="view-inventory-thresholds"`,
		`id="warehouse-check"`,
		`src="./assets/xlwms.js`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("console index does not contain %q", required)
		}
	}
	if strings.Contains(body, "当前用户") {
		t.Fatal("public fulfillment console still presents a logged-in user")
	}
}

func TestStandaloneConsoleHasCSSFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Server{}).localDashboardCSS(recorder, httptest.NewRequest(http.MethodGet, "/temu/dashboard.css", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("fallback stylesheet status = %d", recorder.Code)
	}
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("fallback stylesheet content type = %q", recorder.Header().Get("Content-Type"))
	}
	if !strings.Contains(recorder.Body.String(), ".app-shell") {
		t.Fatal("fallback stylesheet does not contain console layout styles")
	}
}

func TestPurchaseSuccessUsesTemuProcessingFlow(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)
	for _, required := range []string{
		`data-view="labels"><svg><use href="#i-truck"/></svg><span>自动处理中</span>`,
		"<h1>自动处理中</h1>",
		"确认并购买面单",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("console index does not contain %q", required)
		}
	}
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"function rememberPlacedOrder",
		"if (isRecentlyPlacedOrder(order.order_no)) return false;",
		`toast(payload.cached ? "该订单已有发货记录，未重复提交" : "面单购买请求已提交，后台将确认物流并等待面单");`,
		`selectView("labels");`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("purchase success flow does not contain %q", required)
		}
	}
	if strings.Contains(source, `showResult("订单 " + state.orderNo + " 在线下单", payload)`) {
		t.Fatal("successful purchase still opens the raw JSON drawer")
	}
}

func TestProcessingStatusCardsAreSelectable(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)
	for _, required := range []string{
		`data-task-filter="placed"`,
		`data-task-filter="checking"`,
		`data-task-filter="label_ready"`,
		`data-task-filter="parcel"`,
		`data-task-filter="processing"`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("processing status cards do not contain %q", required)
		}
	}
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"function taskMatchesFilter",
		"function setTaskFilter",
		`if (filter === "placed") return status === "placed";`,
		`if (filter === "parcel") return canComplementaryUploadLabel(task) || canStartManualParcel(task);`,
		"function canStartManualParcel",
		"function parcelNeedsComplementaryUpload",
		"Number(task.outbound_status) === 7",
		`if (filter === "label_ready") return status === "label_ready" && !task.parcel_complete;`,
		"function sheinOrderAlreadyCollected",
		"function canComplementaryUploadLabel",
		"sheinOrderAlreadyCollected(task) || !canCreateManualParcel(task)",
		"parcel_complete",
		`byId("task-status-filters").addEventListener("click"`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("processing status filter does not contain %q", required)
		}
	}
}

func TestProcessingViewLetsOperatorsSelectATask(t *testing.T) {
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"function selectProcessingTask",
		`data-task-key="`,
		"if (task) selectProcessingTask(task);",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("processing selection does not contain %q", required)
		}
	}
	css, err := webFiles.ReadFile("web/platform.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "#task-rows tr.is-selected") {
		t.Fatal("processing rows have no selected style")
	}
}

func TestProcessingViewExposesManualParcelCreateForDPS(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)
	for _, required := range []string{
		`id="parcel-create-panel"`,
		`id="parcel-create-form"`,
		`id="create-xlwms-parcel"`,
		`id="upload-xlwms-label"`,
		`id="refresh-parcel-draft"`,
		`id="parcel-sales-platform"`,
		`id="parcel-store-name"`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("parcel create UI does not contain %q", required)
		}
	}
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"function shopRequiresManualParcelCreate",
		"function isDPSFulfillmentWarehouse",
		`data-parcel-task="`,
		`data-upload-label-task="`,
		`byId("parcel-create-form").addEventListener("submit"`,
		`orders/" + encodeURIComponent(orderNo) + "/xlwms-parcel"`,
		`orders/" + encodeURIComponent(orderNo) + "/xlwms-parcel-label"`,
		"Upload_Shipping_Label",
		`toast(outboundNo ? "领星出库单已补建 · " + outboundNo : "领星出库单已补建")`,
		"function omsWarehouseCell",
		"byId(\"upload-xlwms-label\").disabled = !draft.can_upload_label",
		"sales_platform: byId(\"parcel-sales-platform\").value.trim()",
		"store_name: byId(\"parcel-store-name\").value.trim()",
		`toast("销售平台和店铺都要填写", true)`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("parcel create flow does not contain %q", required)
		}
	}
}

func TestProcessingViewCanSearchPlacedOrders(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `id="task-search"`) {
		t.Fatal("processing view has no search box")
	}
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"function taskSearchText",
		"if (query && !taskSearchText(task).includes(query)) return false;",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("processing search does not contain %q", required)
		}
	}
}

func TestManualOrdersShowWarehouseSKUOnly(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "仓库 SKU / 数量") {
		t.Fatal("manual order table does not identify the warehouse SKU column")
	}
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"const goodsHTML = warehouseGoodsLines(order).map",
		"quantityText(line.quantity)",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("manual order warehouse SKU rendering does not contain %q", required)
		}
	}
}

func TestProcessingViewSupportsManualResolution(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)
	for _, required := range []string{
		`id="task-exception-filter"`,
		`option value="failed"`,
		`option value="resolved"`,
		`id="task-resolution-dialog"`,
		`id="task-resolution-reason"`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("manual resolution UI does not contain %q", required)
		}
	}
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"function openTaskResolution",
		"function submitTaskResolution",
		`data-resolve-task=`,
		`"/resolve"`,
		"resolution_reason",
		`byId("task-exception-filter").addEventListener("change"`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("manual resolution flow does not contain %q", required)
		}
	}
}

func TestTransitionNoticeStaysHiddenUntilRequired(t *testing.T) {
	css, err := webFiles.ReadFile("web/platform.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".transition-notice[hidden]") {
		t.Fatal("platform CSS still lets the self-ship notice override the hidden attribute")
	}
}

func TestXLWMSConsoleAssetsAreEmbedded(t *testing.T) {
	server := &Server{}
	for _, asset := range []struct {
		name        string
		contentType string
	}{
		{name: "xlwms.js", contentType: "text/javascript"},
		{name: "platform.css", contentType: "text/css"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/assets/"+asset.name, nil)
		request.SetPathValue("name", asset.name)
		recorder := httptest.NewRecorder()
		server.asset(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("asset %s status = %d", asset.name, recorder.Code)
		}
		if !strings.HasPrefix(recorder.Header().Get("Content-Type"), asset.contentType) {
			t.Fatalf("asset %s Content-Type = %q", asset.name, recorder.Header().Get("Content-Type"))
		}
	}
}

func TestFulfillmentConsoleBuysPlatformLabelsWithoutTransition(t *testing.T) {
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"function canPurchasePlatformLabel",
		"function requiresAddressTransition",
		"待购买面单",
		"shipping/warehouses",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("fulfillment console does not contain %q", required)
		}
	}
	if !strings.Contains(source, "if (requiresAddressTransition(state.detail || {}))") {
		t.Fatal("fulfillment dialog still treats every pending order as an address transition")
	}
	if strings.Contains(source, "if (status === \"1\") {\n      setFulfillmentTransitionRequired(true)") {
		t.Fatal("pending orders still force export-address before warehouse selection")
	}
}

func TestFulfillmentConsoleKeepsPGWarehousesUnselectable(t *testing.T) {
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"function isAllowedShippingWarehouse",
		"function isPGWarehouse",
		"function isCODOrder",
		"PG仓不在实际发货范围内，不能选择",
		"非实际发货仓，不能用于平台面单",
		"if (isCODOrder(state.detail) && ids.length) data.prePackageInfo",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("fulfillment console does not contain %q", required)
		}
	}
}

func TestCarrierPolicyConsoleHasMovedToXLWMS(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(index)
	for _, removed := range []string{
		`data-view="warehouses"`,
		`id="carrier-policy-grid"`,
	} {
		if strings.Contains(html, removed) {
			t.Fatalf("moved carrier policy console still contains %q", removed)
		}
	}
	recorder := httptest.NewRecorder()
	fulfillmentPoliciesMoved(recorder, httptest.NewRequest(http.MethodGet, "/api/carrier-policies", nil))
	if recorder.Code != http.StatusGone {
		t.Fatalf("moved carrier policy endpoint status = %d", recorder.Code)
	}
}

func TestBulkFulfillmentConsoleShowsNonBlockingFailures(t *testing.T) {
	index, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "单个订单异常不会阻塞后续发货") {
		t.Fatal("exception view does not explain order-level failure isolation")
	}
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"completed_with_errors", "批次已处理完成", "异常 "} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("bulk completion UI does not contain %q", required)
		}
	}
}

func TestInventoryThresholdConsoleIsPlatformScoped(t *testing.T) {
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"function loadInventoryThresholds",
		"SHEIN 平台默认安全线已保存",
		"已恢复 SHEIN 平台默认安全线",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("inventory threshold console does not contain %q", required)
		}
	}
	if strings.Contains(source, "inventory-thresholds/defaults/reset") {
		t.Fatal("inventory threshold console still exposes shop-default reset")
	}
}

func TestShopSelectorUsesSharedStoreContract(t *testing.T) {
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"data.default_shop", "shop.code", "shop.name"} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("shop selector does not contain %q", required)
		}
	}
	for _, legacy := range []string{"default_shop_key", "shop.shop_key", "credentials_ready"} {
		if strings.Contains(string(script), legacy) {
			t.Fatalf("shop selector still contains legacy field %q", legacy)
		}
	}
}

func TestXLWMSAccountLookupWaitsForShopInitialization(t *testing.T) {
	appScript, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	xlwmsScript, err := webFiles.ReadFile("web/xlwms.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(appScript), "window.sheinShopReady = loadStatus()") {
		t.Fatal("shop initialization promise is not exposed")
	}
	for _, required := range []string{
		"Promise.resolve(window.sheinShopReady)", "accountRetryTimer", "retryDelay", "领星账户已掉线",
		"function loadOMSPlatformOrders", "oms-platform-orders?status=",
	} {
		if !strings.Contains(string(xlwmsScript), required) {
			t.Fatalf("XLWMS startup recovery does not contain %q", required)
		}
	}
}
