package sheinconsole

import (
	"net/http"
	"net/url"
	"testing"
)

func TestOrderRoutesMatchTemuStyleContract(t *testing.T) {
	server := &Server{}
	mux := http.NewServeMux()
	server.registerRoutes(mux)
	tests := []struct {
		method  string
		path    string
		pattern string
	}{
		{http.MethodGet, "/api/orders", "GET /api/orders"},
		{http.MethodGet, "/api/orders/history", "GET /api/orders/history"},
		{http.MethodGet, "/api/orders/ORDER-1", "GET /api/orders/{orderNo}"},
		{http.MethodGet, "/api/orders/ORDER-1/detail", "GET /api/orders/{orderNo}/detail"},
		{http.MethodPost, "/api/orders/sync", "POST /api/orders/sync"},
		{http.MethodPost, "/api/orders/details/sync", "POST /api/orders/details/sync"},
	}
	for _, test := range tests {
		request := &http.Request{Method: test.method, URL: &url.URL{Path: test.path}}
		_, pattern := mux.Handler(request)
		if pattern != test.pattern {
			t.Errorf("%s %s matched %q, want %q", test.method, test.path, pattern, test.pattern)
		}
	}
}

func TestOrderPaginationUsesTemuCompatibleBounds(t *testing.T) {
	request := &http.Request{URL: &url.URL{RawQuery: "page=3&page_size=75"}}
	page, pageSize := orderPagination(request)
	if page != 3 || pageSize != 75 {
		t.Fatalf("pagination = (%d, %d), want (3, 75)", page, pageSize)
	}

	request.URL.RawQuery = "page=0&page_size=101"
	page, pageSize = orderPagination(request)
	if page != 1 || pageSize != 30 {
		t.Fatalf("bounded pagination = (%d, %d), want defaults", page, pageSize)
	}
}
