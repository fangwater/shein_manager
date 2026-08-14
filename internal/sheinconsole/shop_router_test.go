package sheinconsole

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShopRouterSelectsRequestedShop(t *testing.T) {
	handlers := map[string]http.Handler{
		"beauty-hangers-home": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("beauty")) }),
		"second-shop":         http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("second")) }),
	}
	router := NewShopRouter("beauty-hangers-home", nil, handlers)
	request := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	request.Header.Set(shopHeader, "second-shop")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Body.String() != "second" {
		t.Fatalf("selected handler returned %q", response.Body.String())
	}
}

func TestShopRouterDefaultsAndListsShops(t *testing.T) {
	router := NewShopRouter("beauty-hangers-home", []ShopInfo{{
		Code: "beauty-hangers-home", Name: "Beauty Hangers home", Default: true,
	}}, map[string]http.Handler{
		"beauty-hangers-home": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("beauty")) }),
	})
	defaultResponse := httptest.NewRecorder()
	router.ServeHTTP(defaultResponse, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if defaultResponse.Body.String() != "beauty" {
		t.Fatalf("default handler returned %q", defaultResponse.Body.String())
	}
	shopsResponse := httptest.NewRecorder()
	router.ServeHTTP(shopsResponse, httptest.NewRequest(http.MethodGet, "/api/system/shops", nil))
	if shopsResponse.Code != http.StatusOK || !strings.Contains(shopsResponse.Body.String(), "Beauty Hangers home") {
		t.Fatalf("unexpected shops response: %d %s", shopsResponse.Code, shopsResponse.Body.String())
	}
}

func TestShopRouterRejectsUnknownShop(t *testing.T) {
	router := NewShopRouter("beauty-hangers-home", nil, map[string]http.Handler{
		"beauty-hangers-home": http.NotFoundHandler(),
	})
	request := httptest.NewRequest(http.MethodGet, "/api/orders", nil)
	request.Header.Set(shopHeader, "missing")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusBadRequest)
	}
}
