package sheinconsole

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestInventoryThresholdRoutesUseCurrentShop(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/inventory-thresholds" {
			http.NotFound(writer, request)
			return
		}
		if request.URL.Query().Get("platform") != "shein" || request.URL.Query().Get("shop") != "beauty-hangers-home" {
			t.Fatalf("query = %s", request.URL.RawQuery)
		}
		if request.Header.Get("X-Shein-Shop") != "beauty-hangers-home" {
			t.Fatalf("X-Shein-Shop = %q", request.Header.Get("X-Shein-Shop"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":{"records":[{"warehouse_sku":"WH-1","east_threshold":1,"west_threshold":2,"total_threshold":3,"customized":false,"source":"shop_default"}],"page":1,"page_size":30,"total":1,"pages":1,"default_thresholds":{"platform":"shein","shop_code":"beauty-hangers-home","east_threshold":10,"west_threshold":20,"total_threshold":30,"customized":false}}}`))
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
