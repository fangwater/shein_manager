package xlwms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientUsesPublicManagerContract(t *testing.T) {
	requests := make(chan *http.Request, 3)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request.Clone(request.Context())
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/platform-orders/accounts":
			_, _ = writer.Write([]byte(`{"success":true,"data":[{"key":"dps","label":"DPS"}]}`))
		case "/api/temu/platform-orders/SHEIN-1":
			_, _ = writer.Write([]byte(`{"success":true,"data":{"account":"dps","platform_order_no":"SHEIN-1","found":true,"match_count":1,"orders":[{"oms_order_no":"OMS-1","platform_order_no":"SHEIN-1","platform_code":"SHEIN","status":2,"status_key":"processing"}],"queried_at":"2026-08-14T00:00:00Z"}}`))
		case "/api/temu/warehouse-availability/query":
			var payload struct {
				Items []InventoryItem `json:"items"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode inventory request: %v", err)
			}
			if len(payload.Items) != 1 || payload.Items[0].WarehouseSKU != "WH-SKU" || payload.Items[0].Quantity != 2 {
				t.Fatalf("inventory payload = %#v", payload)
			}
			_, _ = writer.Write([]byte(`{"success":true,"data":{"complete":true,"records":[]}}`))
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

	<-requests
	orderRequest := <-requests
	if orderRequest.Header.Get("X-OMS-Account") != "dps" {
		t.Fatalf("X-OMS-Account = %q", orderRequest.Header.Get("X-OMS-Account"))
	}
	<-requests
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
