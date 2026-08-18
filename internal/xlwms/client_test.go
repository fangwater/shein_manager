package xlwms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientUsesPublicManagerContract(t *testing.T) {
	requests := make(chan *http.Request, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request.Clone(request.Context())
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/platform-orders/accounts":
			_, _ = writer.Write([]byte(`{"success":true,"data":[{"key":"dps","label":"DPS","available":true,"status":"ready"}]}`))
		case "/api/temu/platform-orders/SHEIN-1":
			_, _ = writer.Write([]byte(`{"success":true,"data":{"account":"dps","platform_order_no":"SHEIN-1","found":true,"match_count":1,"orders":[{"oms_order_no":"OMS-1","platform_order_no":"SHEIN-1","platform_code":"SHEIN","status":2,"status_key":"processing"}],"queried_at":"2026-08-14T00:00:00Z"}}`))
		case "/api/temu/warehouse-availability/query":
			var payload struct {
				Items    []InventoryItem `json:"items"`
				Platform string          `json:"platform"`
				ShopCode string          `json:"shop_code"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode inventory request: %v", err)
			}
			if len(payload.Items) != 1 || payload.Items[0].WarehouseSKU != "WH-SKU" || payload.Items[0].Quantity != 2 {
				t.Fatalf("inventory payload = %#v", payload)
			}
			if request.Header.Get("X-Shein-Shop") != "" || payload.Platform != "" || payload.ShopCode != "" {
				t.Fatalf("unscoped inventory request leaked shop identity: %#v headers=%v", payload, request.Header)
			}
			_, _ = writer.Write([]byte(`{"success":true,"data":{"complete":true,"records":[]}}`))
		case "/api/inventory-thresholds/defaults":
			if request.URL.Query().Get("platform") != "shein" || request.URL.Query().Get("shop") != "beauty-hangers-home" {
				t.Fatalf("shop query = %s", request.URL.RawQuery)
			}
			if request.Header.Get("X-Shein-Shop") != "beauty-hangers-home" {
				t.Fatalf("X-Shein-Shop = %q", request.Header.Get("X-Shein-Shop"))
			}
			_, _ = writer.Write([]byte(`{"success":true,"data":{"platform":"shein","shop_code":"beauty-hangers-home","east_threshold":10,"west_threshold":20,"total_threshold":30,"customized":true}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL+"/api/", time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	accounts, err := client.Accounts(context.Background())
	if err != nil || len(accounts) != 1 || accounts[0].Key != "dps" {
		t.Fatalf("Accounts = %#v, %v", accounts, err)
	}
	lookup, err := client.QueryPlatformOrder(context.Background(), "dps", "SHEIN-1")
	if err != nil || !lookup.Found || len(lookup.Orders) != 1 {
		t.Fatalf("QueryPlatformOrder = %#v, %v", lookup, err)
	}
	if _, err := client.QueryInventory(context.Background(), []InventoryItem{{WarehouseSKU: "WH-SKU", Quantity: 2}}); err != nil {
		t.Fatalf("QueryInventory: %v", err)
	}
	thresholds, err := client.ShopInventoryThresholds(context.Background(), "shein", "beauty-hangers-home")
	if err != nil || thresholds.ShopCode != "beauty-hangers-home" || thresholds.EastThreshold != 10 {
		t.Fatalf("ShopInventoryThresholds = %#v, %v", thresholds, err)
	}

	<-requests
	orderRequest := <-requests
	if orderRequest.Header.Get("X-OMS-Account") != "dps" {
		t.Fatalf("X-OMS-Account = %q", orderRequest.Header.Get("X-OMS-Account"))
	}
	<-requests
	<-requests
}

func TestQueryInventoryForShopSendsShopIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/temu/warehouse-availability/query" {
			http.NotFound(writer, request)
			return
		}
		var payload struct {
			Platform string `json:"platform"`
			ShopCode string `json:"shop_code"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Platform != "shein" || payload.ShopCode != "beauty-hangers-home" {
			t.Fatalf("payload = %#v", payload)
		}
		if request.Header.Get("X-Shein-Shop") != "beauty-hangers-home" {
			t.Fatalf("X-Shein-Shop = %q", request.Header.Get("X-Shein-Shop"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":{"complete":true,"records":[]}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.QueryInventoryForShop(context.Background(), "shein", "beauty-hangers-home", []InventoryItem{{WarehouseSKU: "WH-SKU", Quantity: 1}}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncFulfillmentAuditsPostsSheinSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/fulfillment-audits/sync" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("X-Shein-Shop") != "beauty-hangers-home" {
			t.Fatalf("X-Shein-Shop = %q", request.Header.Get("X-Shein-Shop"))
		}
		var payload FulfillmentAuditSnapshot
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Platform != "shein" || payload.ShopCode != "beauty-hangers-home" || len(payload.Orders) != 1 || payload.Orders[0].PlatformOrderNo != "GSU-1" {
			t.Fatalf("payload = %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":{"orders":1,"matched":1}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SyncFulfillmentAudits(context.Background(), FulfillmentAuditSnapshot{
		Platform: "shein", ShopCode: "beauty-hangers-home", ShopName: "Beauty Hangers home",
		Orders: []FulfillmentAuditSnapshotOrder{{PlatformOrderNo: "GSU-1", WarehouseCode: "DPSNY002"}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateParcelPostsWarehouseScopedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/outbound/parcel-create" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		var payload struct {
			Warehouse string `json:"warehouse"`
			Data      []struct {
				Warehouse       string `json:"whCode"`
				PlatformOrderNo string `json:"platformOrderNo"`
			} `json:"data"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Warehouse != "DPSNY002" || len(payload.Data) != 1 || payload.Data[0].Warehouse != "DPSNY002" || payload.Data[0].PlatformOrderNo != "GSU-1" {
			t.Fatalf("payload = %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":[{"outboundOrderNo":"OB-1"}]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.CreateParcel(context.Background(), "DPSNY002", []any{map[string]any{
		"whCode": "DPSNY002", "platformOrderNo": "GSU-1", "salesPlatform": "SHEIN",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `[{"outboundOrderNo":"OB-1"}]` {
		t.Fatalf("result = %s", result)
	}
}

func TestUpdateParcelTrackingLabelPostsOfficialFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/outbound/tracking-label-update" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		var payload struct {
			Warehouse string `json:"warehouse"`
			Data      struct {
				OutboundOrderNo string `json:"outboundOrderNo"`
				TrackingNumber  string `json:"trackingNumber"`
				LabelURL        string `json:"labelUrl"`
				LabelFileName   string `json:"labelFileName"`
				LabelType       string `json:"labelType"`
			} `json:"data"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Warehouse != "DPSNY002" || payload.Data.OutboundOrderNo != "OB-1" ||
			payload.Data.TrackingNumber != "1Z999" || payload.Data.LabelURL != "https://pdf.example/label.pdf" ||
			payload.Data.LabelFileName != "GSU-1.pdf" || payload.Data.LabelType != "pdf" {
			t.Fatalf("payload = %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":true}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.UpdateParcelTrackingLabel(context.Background(), "DPSNY002", map[string]any{
		"outboundOrderNo": "OB-1", "trackingNumber": "1Z999",
		"labelUrl": "https://pdf.example/label.pdf", "labelFileName": "GSU-1.pdf", "labelType": "pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `true` {
		t.Fatalf("result = %s", result)
	}
}

func TestCancelParcelPostsOutboundOrderList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/outbound/parcel-cancel" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		var payload struct {
			Warehouse string `json:"warehouse"`
			Data      struct {
				OutboundOrderNoList []string `json:"outboundOrderNoList"`
			} `json:"data"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Warehouse != "DPSNY002" || len(payload.Data.OutboundOrderNoList) != 1 || payload.Data.OutboundOrderNoList[0] != "OB-1" {
			t.Fatalf("payload = %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":true}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.CancelParcel(context.Background(), "DPSNY002", []string{"OB-1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `true` {
		t.Fatalf("result = %s", result)
	}
}

func TestLookupParcelPostsThirdOrderFilter(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		if request.Method != http.MethodPost || request.URL.Path != "/outbound/parcel-detail" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		var payload struct {
			Warehouse string `json:"warehouse"`
			Data      struct {
				ThirdOrderNoList []string `json:"thirdOrderNoList"`
				ReferOrderNoList []string `json:"referOrderNoList"`
			} `json:"data"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Warehouse != "DPSNY002" || len(payload.Data.ThirdOrderNoList) != 1 || payload.Data.ThirdOrderNoList[0] != "GSU-1" {
			t.Fatalf("payload = %#v", payload)
		}
		if len(payload.Data.ReferOrderNoList) != 0 {
			t.Fatalf("third-order hit must not fall back: %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":{"code":200,"data":[{"outboundOrderNo":"OB-9","thirdOrderNo":"GSU-1"}]}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.LookupParcel(context.Background(), "DPSNY002", "GSU-1", "GSU-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"OB-9"`) || len(paths) != 1 {
		t.Fatalf("result = %s paths=%v", result, paths)
	}
}

func TestLookupParcelFallsBackToReferOrderFilter(t *testing.T) {
	var filters []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Data struct {
				ThirdOrderNoList []string `json:"thirdOrderNoList"`
				ReferOrderNoList []string `json:"referOrderNoList"`
			} `json:"data"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		if len(payload.Data.ThirdOrderNoList) == 1 {
			filters = append(filters, "third")
			_, _ = writer.Write([]byte(`{"success":true,"data":{"code":200,"data":[],"msg":"操作成功"}}`))
			return
		}
		if len(payload.Data.ReferOrderNoList) == 1 && payload.Data.ReferOrderNoList[0] == "GSU-1" {
			filters = append(filters, "refer")
			_, _ = writer.Write([]byte(`{"success":true,"data":{"code":200,"data":[{"outboundOrderNo":"OB-OLD","referOrderNo":"GSU-1","status":4}]}}`))
			return
		}
		t.Fatalf("unexpected payload %#v", payload)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.LookupParcel(context.Background(), "DPSNY002", "GSU-1", "GSU-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(filters, ",") != "third,refer" || !strings.Contains(string(result), `"OB-OLD"`) {
		t.Fatalf("filters=%v result=%s", filters, result)
	}
}

func TestParcelCancelStatusPostsOutboundOrderList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/outbound/cancel-status" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		var payload struct {
			Warehouse string `json:"warehouse"`
			Data      struct {
				OutboundOrderNoList []string `json:"outboundOrderNoList"`
			} `json:"data"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Warehouse != "DPSNY002" || len(payload.Data.OutboundOrderNoList) != 1 || payload.Data.OutboundOrderNoList[0] != "OB-1" {
			t.Fatalf("payload = %#v", payload)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"data":[{"outboundOrderNo":"OB-1","status":1,"msg":"自动拦截处理成功"}]}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ParcelCancelStatus(context.Background(), "DPSNY002", []string{"OB-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), `"status":1`) {
		t.Fatalf("result = %s", result)
	}
}

func TestNewClientRejectsRelativeURL(t *testing.T) {
	if _, err := NewClient("/warehouse-console/api", time.Second); err == nil {
		t.Fatal("relative XLWMS URL was accepted")
	}
}

func TestClientDoesNotAcceptFailedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"success":false,"error":"service unavailable"}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Accounts(context.Background())
	if err == nil {
		t.Fatal("failed XLWMS envelope was accepted")
	}
	if gateway, ok := err.(*GatewayError); !ok || gateway.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("error = %#v", err)
	}
}
