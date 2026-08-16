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
		"PG仓不在实际发货范围内，不能选择",
		"非实际发货仓，不能用于平台面单",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("fulfillment console does not contain %q", required)
		}
	}
}

func TestInventoryThresholdConsoleIsShopScoped(t *testing.T) {
	script, err := webFiles.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(script)
	for _, required := range []string{
		"function loadInventoryThresholds",
		"inventory-thresholds/defaults/reset",
		"当前店铺默认安全线已保存",
		"当前店铺已恢复仓库默认安全线",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("inventory threshold console does not contain %q", required)
		}
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
	for _, required := range []string{"Promise.resolve(window.sheinShopReady)", "accountRetryTimer", "retryDelay"} {
		if !strings.Contains(string(xlwmsScript), required) {
			t.Fatalf("XLWMS startup recovery does not contain %q", required)
		}
	}
}
