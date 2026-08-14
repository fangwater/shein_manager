package sheinconsole

import (
	"net/http"
	"strings"
)

type ShopInfo struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

type ShopRouter struct {
	defaultCode string
	shops       []ShopInfo
	handlers    map[string]http.Handler
}

func NewShopRouter(defaultCode string, shops []ShopInfo, handlers map[string]http.Handler) http.Handler {
	return securityHeaders(&ShopRouter{defaultCode: defaultCode, shops: shops, handlers: handlers})
}

func (router *ShopRouter) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/api/system/shops" {
		writer.Header().Set("Cache-Control", "no-store")
		writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]any{
			"default_shop": router.defaultCode,
			"shops":        router.shops,
		}})
		return
	}
	code := strings.TrimSpace(request.Header.Get(shopHeader))
	if code == "" {
		code = router.defaultCode
	}
	handler, ok := router.handlers[code]
	if !ok {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "未知或未启用的店铺"})
		return
	}
	handler.ServeHTTP(writer, request)
}
