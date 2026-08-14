package sheinconsole

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestedShopKeyUsesRoutedShop(t *testing.T) {
	server := &Server{shopKey: "beauty-hangers-home"}

	tests := []struct {
		name    string
		header  string
		payload string
	}{
		{name: "header", header: "beauty-hangers-home", payload: "beauty-hangers-home"},
		{name: "payload", payload: "beauty-hangers-home"},
		{name: "configured shop"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/order/list", nil)
			request.Header.Set(shopHeader, test.header)
			got, err := server.requestedShopKey(request, test.payload)
			if err != nil {
				t.Fatalf("requestedShopKey returned an error: %v", err)
			}
			if got != "beauty-hangers-home" {
				t.Fatalf("requestedShopKey = %q, want beauty-hangers-home", got)
			}
		})
	}
}

func TestRequestedShopKeyRejectsMismatch(t *testing.T) {
	server := &Server{shopKey: "beauty-hangers-home"}
	request := httptest.NewRequest(http.MethodPost, "/api/order/list", nil)
	request.Header.Set(shopHeader, "other-shop")

	_, err := server.requestedShopKey(request, "")
	if err == nil || !strings.Contains(err.Error(), shopHeader) {
		t.Fatalf("requestedShopKey error = %v, want header mismatch", err)
	}
}

func TestRequestedShopKeyRejectsPayloadForAnotherRoute(t *testing.T) {
	server := &Server{shopKey: "beauty-hangers-home"}
	request := httptest.NewRequest(http.MethodPost, "/api/order/list", nil)
	_, err := server.requestedShopKey(request, "other-shop")
	if err == nil || !strings.Contains(err.Error(), "shop_key") {
		t.Fatalf("requestedShopKey error = %v, want payload mismatch", err)
	}
}

func TestConsoleRoutesDoNotRequireLogin(t *testing.T) {
	handler := (&Server{}).routes()
	tests := []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{method: http.MethodGet, path: "/", want: http.StatusOK},
		{method: http.MethodPost, path: "/api/order/list", body: "{", want: http.StatusBadRequest},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Errorf("%s %s status = %d, want %d", test.method, test.path, recorder.Code, test.want)
		}
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
