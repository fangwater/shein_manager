package sheinconsole

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestedShopKeyPrecedence(t *testing.T) {
	server := &Server{defaultShopKey: "default-shop"}

	tests := []struct {
		name        string
		header      string
		payload     string
		defaultShop string
		want        string
	}{
		{name: "header", header: "header-shop", payload: "header-shop", want: "header-shop"},
		{name: "payload", payload: "payload-shop", want: "payload-shop"},
		{name: "configured default", want: "default-shop"},
		{name: "fallback default", defaultShop: " ", want: "default"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := *server
			if test.defaultShop != "" {
				current.defaultShopKey = test.defaultShop
			}
			request := httptest.NewRequest(http.MethodPost, "/api/order/list", nil)
			request.Header.Set(shopHeader, test.header)
			got, err := current.requestedShopKey(request, test.payload)
			if err != nil {
				t.Fatalf("requestedShopKey returned an error: %v", err)
			}
			if got != test.want {
				t.Fatalf("requestedShopKey = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRequestedShopKeyRejectsMismatch(t *testing.T) {
	server := &Server{defaultShopKey: "default-shop"}
	request := httptest.NewRequest(http.MethodPost, "/api/order/list", nil)
	request.Header.Set(shopHeader, "header-shop")

	_, err := server.requestedShopKey(request, "payload-shop")
	if err == nil || !strings.Contains(err.Error(), shopHeader) {
		t.Fatalf("requestedShopKey error = %v, want header mismatch", err)
	}
}

func TestEmbeddedConsoleAssets(t *testing.T) {
	server := &Server{}
	tests := []struct {
		name        string
		contentType string
	}{
		{name: "app.js", contentType: "text/javascript"},
		{name: "styles.css", contentType: "text/css"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/assets/"+test.name, nil)
			request.SetPathValue("name", test.name)
			recorder := httptest.NewRecorder()
			server.asset(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("asset status = %d", recorder.Code)
			}
			if !strings.HasPrefix(recorder.Header().Get("Content-Type"), test.contentType) {
				t.Fatalf("Content-Type = %q", recorder.Header().Get("Content-Type"))
			}
			if recorder.Body.Len() == 0 {
				t.Fatal("embedded asset is empty")
			}
		})
	}
}
