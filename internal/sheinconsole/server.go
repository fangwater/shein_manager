package sheinconsole

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"shein-api-manager/internal/shein"
)

//go:embed web/*
var webFiles embed.FS

const shopHeader = "X-Shein-Shop"

type Server struct {
	store          *shein.Store
	verifier       *shein.SessionVerifier
	defaultShopKey string
	requestTimeout time.Duration
	logger         *slog.Logger
}

type response struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	Code    string `json:"code,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
	Cached  bool   `json:"cached,omitempty"`
}

type proxyRequest struct {
	ShopKey string         `json:"shop_key"`
	Data    map[string]any `json:"data"`
}

type userContextKey struct{}

func New(store *shein.Store, verifier *shein.SessionVerifier, defaultShopKey string, requestTimeout time.Duration, logger *slog.Logger) http.Handler {
	server := &Server{
		store: store, verifier: verifier, defaultShopKey: defaultShopKey,
		requestTimeout: requestTimeout, logger: logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.health)
	mux.Handle("GET /", server.requireAuth(http.HandlerFunc(server.index)))
	mux.Handle("GET /assets/{name}", server.requireAuth(http.HandlerFunc(server.asset)))
	mux.Handle("GET /api/status", server.requireAuth(http.HandlerFunc(server.status)))
	mux.Handle("GET /api/system/shops", server.requireAuth(http.HandlerFunc(server.status)))
	mux.Handle("POST /api/order/list", server.requireAuth(server.operationHandler("order-list")))
	mux.Handle("POST /api/order/detail", server.requireAuth(server.operationHandler("order-detail")))
	mux.Handle("POST /api/order/export-address", server.requireAuth(server.operationHandler("export-address")))
	mux.Handle("GET /api/shipping/tasks", server.requireAuth(http.HandlerFunc(server.fulfillmentTasks)))
	mux.Handle("POST /api/shipping/warehouses", server.requireAuth(server.operationHandler("available-shipping-warehouse")))
	mux.Handle("POST /api/shipping/channels", server.requireAuth(server.operationHandler("order-mapping-channels")))
	mux.Handle("POST /api/shipping/place", server.requireAuth(server.operationHandler("place-express-order")))
	mux.Handle("POST /api/shipping/check", server.requireAuth(server.operationHandler("check-express-order")))
	mux.Handle("POST /api/shipping/label", server.requireAuth(server.operationHandler("print-express-info")))
	mux.Handle("GET /api/shipping/track", server.requireAuth(http.HandlerFunc(server.logisticsTrack)))
	return securityHeaders(mux)
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]string{"status": "ok", "service": "shein-go-manager"}})
}

func (s *Server) index(writer http.ResponseWriter, _ *http.Request) {
	content, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(writer, "SHEIN console is unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(content)
}

func (s *Server) asset(writer http.ResponseWriter, request *http.Request) {
	name := request.PathValue("name")
	contentType := ""
	switch name {
	case "app.js":
		contentType = "text/javascript; charset=utf-8"
	case "styles.css":
		contentType = "text/css; charset=utf-8"
	default:
		http.NotFound(writer, request)
		return
	}
	content, err := webFiles.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(content)
}

func (s *Server) status(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	shops, err := s.store.ListShops(ctx)
	if err != nil {
		s.internalError(writer, "list shops", err)
		return
	}
	shopKey, err := s.requestedShopKey(request, "")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]any{
		"service": "shein-go-manager", "user": authenticatedUser(request.Context()),
		"default_shop_key": s.defaultShopKey, "current_shop_key": shopKey, "shops": shops, "endpoints": len(shein.Endpoints),
	}})
}

func (s *Server) fulfillmentTasks(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	tasks, err := s.store.ListFulfillmentTasks(ctx, shopKey, 200)
	if err != nil {
		s.internalError(writer, "list fulfillment tasks", err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: tasks})
}

func (s *Server) operationHandler(operation string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload proxyRequest
		if !decodeJSON(writer, request, &payload) {
			return
		}
		if err := shein.Validate(operation, payload.Data); err != nil {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		shopKey, err := s.requestedShopKey(request, payload.ShopKey)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		confirmValue, requiresIdempotency, err := requiredConfirmation(operation, payload.Data)
		if err != nil {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		if confirmValue != "" && request.Header.Get("X-Confirm-Shein-Action") != confirmValue {
			writeJSON(writer, http.StatusPreconditionRequired, response{Success: false, Error: "explicit action confirmation is required"})
			return
		}
		idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
		requestHash := hashRequest(payload.Data)
		ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
		defer cancel()
		if requiresIdempotency {
			record, reserved, reserveErr := s.store.ReserveOperation(ctx, shopKey, operation, idempotencyKey, requestHash)
			if reserveErr != nil {
				writeJSON(writer, http.StatusConflict, response{Success: false, Error: reserveErr.Error()})
				return
			}
			if !reserved {
				switch record.Status {
				case "completed":
					s.persistFulfillmentState(shopKey, operation, payload.Data, record.Response)
					writeJSON(writer, http.StatusOK, response{Success: true, Data: record.Response, Cached: true})
				case "pending":
					writeJSON(writer, http.StatusConflict, response{Success: false, Error: "the same operation is already in progress"})
				default:
					writeJSON(writer, http.StatusConflict, response{Success: false, Error: "the previous operation failed; review it before using a new idempotency key"})
				}
				return
			}
		}
		credentials, err := s.store.Credentials(ctx, shopKey)
		if err != nil {
			if requiresIdempotency {
				_ = s.store.FailOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, operationErrorSummary(err))
			}
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
			return
		}
		if operation == "place-express-order" {
			recorded, snapshotErr := s.store.ReserveLabelPurchaseChoice(
				ctx, shopKey, firstString(payload.Data, "preRequestId"),
				firstString(payload.Data, "expressChannelCode"), idempotencyKey,
			)
			if snapshotErr != nil {
				_ = s.store.FailOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, operationErrorSummary(snapshotErr))
				s.logger.Error("reserve SHEIN price snapshot failed", "shop", shopKey, "error", sanitizedError(snapshotErr))
				writeJSON(writer, http.StatusConflict, response{Success: false, Error: "无法保存购单价格快照，请重新查询物流渠道"})
				return
			}
			if !recorded {
				s.logger.Info("SHEIN legacy quote has no price snapshot", "shop", shopKey)
			}
		}
		started := time.Now()
		result, err := shein.NewClient(credentials, s.requestTimeout).Call(ctx, operation, payload.Data)
		if err != nil {
			if requiresIdempotency {
				_ = s.store.FailOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, operationErrorSummary(err))
			}
			s.writeAPIError(writer, err)
			s.logger.Warn("SHEIN operation failed", "operation", operation, "shop", shopKey, "user", authenticatedUser(request.Context()), "duration_ms", time.Since(started).Milliseconds(), "error", sanitizedError(err))
			return
		}
		if operation == "order-mapping-channels" {
			if err := s.saveShippingQuote(shopKey, payload.Data, result); err != nil {
				s.internalError(writer, "save shipping quote", err)
				return
			}
		}
		if requiresIdempotency {
			storedResult := cacheableOperationResponse(operation, result)
			if err := s.store.CompleteOperation(context.WithoutCancel(ctx), shopKey, operation, idempotencyKey, storedResult); err != nil {
				s.internalError(writer, "save operation result", err)
				return
			}
		}
		s.persistFulfillmentState(shopKey, operation, payload.Data, result)
		s.logger.Info("SHEIN operation completed", "operation", operation, "shop", shopKey, "user", authenticatedUser(request.Context()), "duration_ms", time.Since(started).Milliseconds())
		writeJSON(writer, http.StatusOK, response{Success: true, Data: result})
	})
}

func (s *Server) logisticsTrack(writer http.ResponseWriter, request *http.Request) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	credentials, err := s.store.Credentials(ctx, shopKey)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	result, err := shein.NewClient(credentials, s.requestTimeout).LogisticsTrack(
		ctx,
		request.URL.Query().Get("orderNo"),
		request.URL.Query().Get("packageNo"),
		request.URL.Query().Get("waybillNo"),
		request.URL.Query().Get("returnOrderNo"),
	)
	if err != nil {
		s.writeAPIError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: result})
}

func (s *Server) requestedShopKey(request *http.Request, payloadShopKey string) (string, error) {
	headerShopKey := strings.TrimSpace(request.Header.Get(shopHeader))
	payloadShopKey = strings.TrimSpace(payloadShopKey)
	if headerShopKey != "" && payloadShopKey != "" && headerShopKey != payloadShopKey {
		return "", errors.New("X-Shein-Shop does not match shop_key")
	}
	if headerShopKey != "" {
		return headerShopKey, nil
	}
	if payloadShopKey != "" {
		return payloadShopKey, nil
	}
	if shopKey := strings.TrimSpace(s.defaultShopKey); shopKey != "" {
		return shopKey, nil
	}
	return "default", nil
}

func requiredConfirmation(operation string, data map[string]any) (string, bool, error) {
	switch operation {
	case "place-express-order":
		return "place-express-order", true, nil
	case "print-express-info":
		return "print-express-info", true, nil
	case "export-address":
		handleType, ok := integerValue(data["handleType"])
		if ok && handleType == 2 {
			return "export-address-transition", true, nil
		}
	}
	return "", false, nil
}

func hashRequest(data map[string]any) string {
	encoded, _ := json.Marshal(data)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie(shein.SessionCookieName)
		if err != nil {
			s.unauthorized(writer, request)
			return
		}
		username, ok := s.verifier.Verify(cookie.Value)
		if !ok {
			s.unauthorized(writer, request)
			return
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), userContextKey{}, username)))
	})
}

func (s *Server) unauthorized(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/api/") {
		writeJSON(writer, http.StatusUnauthorized, response{Success: false, Error: "SHEIN login is required"})
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(writer, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>需要登录</title><style>body{margin:0;font-family:system-ui;background:#f4f6f8;color:#17202a;display:grid;place-items:center;min-height:100vh}main{width:min(420px,calc(100% - 40px));border:1px solid #d8dee5;background:#fff;padding:28px;border-radius:6px}h1{font-size:22px;margin:0 0 10px}p{color:#5d6875;line-height:1.6}a{display:inline-block;margin-top:10px;background:#111820;color:#fff;padding:10px 16px;text-decoration:none;border-radius:4px}</style><main><h1>SHEIN 登录已失效</h1><p>请先回到现有 SHEIN 管理台完成登录，再打开此页面。</p><a href="/">前往登录</a></main></html>`)
}

func authenticatedUser(ctx context.Context) string {
	value, _ := ctx.Value(userContextKey{}).(string)
	return value
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 2<<20))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "invalid JSON request"})
		return false
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "request body must contain one JSON object"})
		return false
	}
	return true
}

func (s *Server) writeAPIError(writer http.ResponseWriter, err error) {
	var apiErr *shein.APIError
	if errors.As(err, &apiErr) {
		writeJSON(writer, http.StatusBadGateway, response{Success: false, Error: apiErr.Message, Code: apiErr.Code, TraceID: apiErr.TraceID})
		return
	}
	writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
}

func (s *Server) internalError(writer http.ResponseWriter, action string, err error) {
	s.logger.Error("SHEIN console internal error", "action", action, "error", sanitizedError(err))
	writeJSON(writer, http.StatusInternalServerError, response{Success: false, Error: "internal service error"})
}

func sanitizedError(err error) string {
	var apiErr *shein.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("code=%s trace_id=%s", apiErr.Code, apiErr.TraceID)
	}
	return err.Error()
}

func operationErrorSummary(err error) string {
	var apiErr *shein.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("code=%s trace_id=%s", apiErr.Code, apiErr.TraceID)
	}
	return "operation failed"
}

func cacheableOperationResponse(operation string, result map[string]any) map[string]any {
	if operation != "export-address" {
		return result
	}
	return map[string]any{
		"code":    result["code"],
		"msg":     result["msg"],
		"traceId": result["traceId"],
		"info":    map[string]any{"redacted": true},
	}
}

func integerValue(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		integer, err := strconv.Atoi(typed.String())
		return integer, err == nil
	case float64:
		integer := int(typed)
		return integer, float64(integer) == typed
	case int:
		return typed, true
	default:
		return 0, false
	}
}

func writeJSON(writer http.ResponseWriter, status int, payload response) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "SAMEORIGIN")
		writer.Header().Set("Referrer-Policy", "same-origin")
		writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data: https:; connect-src 'self'; frame-ancestors 'self'")
		next.ServeHTTP(writer, request)
	})
}
