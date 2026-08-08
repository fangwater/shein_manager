package shein

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLogisticsTrackSupportsWaybillAndReturnOrderQueries(t *testing.T) {
	tests := []struct {
		orderNo       string
		waybillNo     string
		returnOrderNo string
		wantQuery     string
	}{
		{orderNo: "ORDER-1", waybillNo: "WAYBILL-1", wantQuery: "orderNo=ORDER-1&waybillNo=WAYBILL-1"},
		{returnOrderNo: "RETURN-1", wantQuery: "returnOrderNo=RETURN-1"},
	}
	for _, test := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.RawQuery != test.wantQuery {
				t.Errorf("query = %q, want %q", request.URL.RawQuery, test.wantQuery)
			}
			_, _ = writer.Write([]byte(`{"code":"0","msg":"OK","info":{}}`))
		}))
		client := NewClient(Credentials{OpenKeyID: "open-key", SecretKey: "secret-key", BaseURL: server.URL}, time.Second)
		client.randomKey = func() (string, error) { return "abcde", nil }
		if _, err := client.LogisticsTrack(context.Background(), test.orderNo, "", test.waybillNo, test.returnOrderNo); err != nil {
			t.Fatal(err)
		}
		server.Close()
	}
}

func TestLogisticsTrackRejectsIncompleteOrMixedQueries(t *testing.T) {
	client := NewClient(Credentials{}, time.Second)
	for _, values := range [][4]string{{"ORDER-1", "", "", ""}, {"ORDER-1", "PKG-1", "", "RETURN-1"}} {
		if _, err := client.LogisticsTrack(context.Background(), values[0], values[1], values[2], values[3]); err == nil {
			t.Fatalf("query %#v must fail", values)
		}
	}
}

func TestAvailableShippingWarehouseAcceptsCurrentAndLegacyShapes(t *testing.T) {
	if err := Validate("available-shipping-warehouse", map[string]any{"orderNo": "ORDER-1"}); err != nil {
		t.Fatal(err)
	}
	if err := Validate("available-shipping-warehouse", map[string]any{"orderNoList": []any{"ORDER-1"}}); err != nil {
		t.Fatal(err)
	}
}
