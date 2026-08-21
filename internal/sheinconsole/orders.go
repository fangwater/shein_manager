package sheinconsole

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"shein-api-manager/internal/shein"
)

func (s *Server) listOrders(writer http.ResponseWriter, request *http.Request) {
	s.listStoredOrders(writer, request, false)
}

func (s *Server) listOrderHistory(writer http.ResponseWriter, request *http.Request) {
	s.listStoredOrders(writer, request, true)
}

func (s *Server) listStoredOrders(writer http.ResponseWriter, request *http.Request, history bool) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	page, pageSize := orderPagination(request)
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	items, total, err := s.store.ListOrders(ctx, shopKey, request.URL.Query().Get("q"), history, page, pageSize)
	if err != nil {
		s.internalError(writer, "list SHEIN orders", err)
		return
	}
	meta := map[string]any{"page": page, "page_size": pageSize, "total": total}
	if !history {
		if syncStatus, syncErr := s.store.LatestOrderSync(ctx, shopKey); syncErr == nil {
			meta["sync"] = syncStatus
		}
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: items, Meta: meta})
}

func (s *Server) getOrder(writer http.ResponseWriter, request *http.Request) {
	shopKey, ok := s.orderRequestShop(writer, request)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	item, err := s.store.GetOrder(ctx, shopKey, request.PathValue("orderNo"))
	if err != nil {
		s.writeOrderError(writer, "get SHEIN order", err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) getOrderDetail(writer http.ResponseWriter, request *http.Request) {
	shopKey, ok := s.orderRequestShop(writer, request)
	if !ok {
		return
	}
	orderNo := strings.TrimSpace(request.PathValue("orderNo"))
	if orderNo == "" {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "order number is required"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	credentials, err := s.store.Credentials(ctx, shopKey)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	if err := s.refreshSingleOrder(ctx, shein.NewClient(credentials, s.requestTimeout), shopKey, orderNo); err != nil {
		s.writeOrderOperationError(writer, "refresh SHEIN order detail", err)
		return
	}
	item, err := s.store.GetOrder(ctx, shopKey, orderNo)
	if err != nil {
		s.writeOrderError(writer, "load refreshed SHEIN order", err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: item})
}

func (s *Server) syncOrders(writer http.ResponseWriter, request *http.Request) {
	shopKey, ok := s.orderRequestShop(writer, request)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	status, err := s.runTrackedOrderSync(ctx, shopKey)
	if err != nil {
		s.writeOrderOperationError(writer, "sync SHEIN orders", err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: status})
}

func (s *Server) syncOrderDetails(writer http.ResponseWriter, request *http.Request) {
	shopKey, ok := s.orderRequestShop(writer, request)
	if !ok {
		return
	}
	limit := queryInt(request, "limit", 10)
	if limit > shein.MaxOrderDetailBatch {
		limit = shein.MaxOrderDetailBatch
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	orderNos, err := s.store.OrderDetailCandidates(ctx, shopKey, limit)
	if err != nil {
		s.internalError(writer, "list SHEIN detail candidates", err)
		return
	}
	if len(orderNos) == 0 {
		writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]int{"completed": 0}})
		return
	}
	completed, err := s.refreshOrderBatch(ctx, shopKey, orderNos)
	if err != nil {
		s.writeOrderOperationError(writer, "sync SHEIN order details", err)
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]int{"completed": completed}})
}

func (s *Server) runTrackedOrderSync(ctx context.Context, shopKey string) (shein.OrderSyncStatus, error) {
	if !s.orderSyncMu.TryLock() {
		return s.store.LatestOrderSync(ctx, shopKey)
	}
	defer s.orderSyncMu.Unlock()
	status, err := s.store.StartOrderSync(ctx, shopKey)
	if err != nil {
		return shein.OrderSyncStatus{}, err
	}
	fetched, syncErr := s.syncOpenOrders(ctx, shopKey)
	if syncErr != nil {
		_ = s.store.FinishOrderSync(context.WithoutCancel(ctx), status.ID, "failed", fetched, sanitizedError(syncErr))
		return shein.OrderSyncStatus{}, syncErr
	}
	if err := s.store.FinishOrderSync(ctx, status.ID, "succeeded", fetched, ""); err != nil {
		return shein.OrderSyncStatus{}, err
	}
	return s.store.LatestOrderSync(ctx, shopKey)
}

func (s *Server) refreshOrderBatch(ctx context.Context, shopKey string, orderNos []string) (int, error) {
	credentials, err := s.store.Credentials(ctx, shopKey)
	if err != nil {
		return 0, err
	}
	values := make([]any, 0, len(orderNos))
	targets := make(map[string]struct{}, len(orderNos))
	for _, orderNo := range orderNos {
		orderNo = strings.TrimSpace(orderNo)
		if orderNo == "" {
			continue
		}
		values = append(values, orderNo)
		targets[orderNo] = struct{}{}
	}
	if len(values) == 0 {
		return 0, nil
	}
	result, err := shein.NewClient(credentials, s.requestTimeout).Call(ctx, "order-detail", map[string]any{"orderNoList": values})
	if err != nil {
		return 0, err
	}
	snapshots := make([]shein.OrderSnapshot, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, detail := range collectOrderObjects(result["info"], true) {
		orderNo := orderNumberFromMap(detail)
		if _, wanted := targets[orderNo]; !wanted {
			continue
		}
		seen[orderNo] = struct{}{}
		snapshots = append(snapshots, shein.OrderSnapshot{
			OrderNo: orderNo, Status: orderStatusFromMap(detail), DetailData: detail,
		})
	}
	if len(seen) != len(targets) {
		missing := make([]string, 0, len(targets)-len(seen))
		for orderNo := range targets {
			if _, ok := seen[orderNo]; !ok {
				missing = append(missing, orderNo)
			}
		}
		sort.Strings(missing)
		return 0, errors.New("订单详情接口未返回目标订单: " + strings.Join(missing, ", "))
	}
	if err := s.store.UpsertOrderSnapshots(ctx, shopKey, snapshots); err != nil {
		return 0, err
	}
	for _, task := range fulfillmentTasksFromOrderDetail(result) {
		if _, wanted := targets[task.OrderNo]; !wanted {
			continue
		}
		if err := s.store.UpsertFulfillmentTask(ctx, shopKey, task); err != nil {
			return 0, err
		}
	}
	return len(snapshots), nil
}

func (s *Server) orderRequestShop(writer http.ResponseWriter, request *http.Request) (string, bool) {
	shopKey, err := s.requestedShopKey(request, request.URL.Query().Get("shop_key"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return "", false
	}
	return shopKey, true
}

func (s *Server) writeOrderError(writer http.ResponseWriter, action string, err error) {
	if errors.Is(err, shein.ErrOrderNotFound) {
		writeJSON(writer, http.StatusNotFound, response{Success: false, Error: "SHEIN order not found"})
		return
	}
	s.internalError(writer, action, err)
}

func (s *Server) writeOrderOperationError(writer http.ResponseWriter, action string, err error) {
	var apiErr *shein.APIError
	if errors.As(err, &apiErr) {
		s.writeAPIError(writer, err)
		return
	}
	s.internalError(writer, action, err)
}

func orderPagination(request *http.Request) (int, int) {
	page := queryInt(request, "page", 1)
	pageSize := queryInt(request, "page_size", 30)
	if pageSize > 100 {
		pageSize = 30
	}
	return page, pageSize
}
