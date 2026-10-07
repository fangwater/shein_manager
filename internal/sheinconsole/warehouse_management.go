package sheinconsole

import (
	"context"
	"errors"
	"net/http"
	"shein-api-manager/internal/shein"
	"strings"
)

type warehouseOption struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	CanShip bool   `json:"can_ship"`
	OMSCode string `json:"oms_code,omitempty"`
	Enabled bool   `json:"enabled,omitempty"`
}

func (s *Server) warehouseBindingContext(ctx context.Context, shop string) (context.Context, error) {
	if s.xlwms == nil {
		return ctx, errors.New("仓库配置服务不可用")
	}
	bindings, err := s.xlwms.WarehouseBindings(ctx, shop)
	if err != nil {
		return ctx, err
	}
	snapshot := map[string]shein.WarehouseMappingSnapshot{}
	for _, b := range bindings {
		if b.PlatformWarehouseID != "" {
			snapshot[b.PlatformWarehouseID] = shein.WarehouseMappingSnapshot{OMSCode: b.OMSCode, Revision: b.Revision, Enabled: b.Effective}
		}
	}
	return shein.WithWarehouseSnapshot(ctx, snapshot), nil
}
func (s *Server) legacyWarehouseBindings(w http.ResponseWriter, r *http.Request) {
	items := []warehouseOption{}
	for id, code := range shein.LegacyWarehouseMappingsForShop(s.shopKey) {
		items = append(items, warehouseOption{ID: id, Name: code, CanShip: true, OMSCode: code, Enabled: true})
	}
	writeJSON(w, 200, response{Success: true, Data: items})
}
func (s *Server) managementWarehouseOptions(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OrderNo string `json:"order_no"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	orderNo := strings.TrimSpace(in.OrderNo)
	if orderNo == "" {
		orders, err := s.store.ListOrderQueue(ctx, s.shopKey, "all")
		if err != nil {
			s.internalError(w, "list verification orders", err)
			return
		}
		for _, o := range orders {
			if shein.CanPurchasePlatformLabel(o.Detail) && !shein.RequiresAddressTransition(o.Detail, o.OrderStatus) {
				orderNo = o.OrderNo
				break
			}
		}
	}
	if orderNo == "" {
		writeJSON(w, 409, response{Success: false, Error: "请选择一笔支持独立履约的平台订单验证仓库"})
		return
	}
	credentials, err := s.store.Credentials(ctx, s.shopKey)
	if err != nil {
		s.internalError(w, "load warehouse credentials", err)
		return
	}
	// Read platform discovery directly: disabled drafts must still be discoverable.
	result, err := shein.NewClient(credentials, s.requestTimeout).Request(ctx, http.MethodPost, shein.AvailableShippingWarehousePath, map[string]any{"orderNo": orderNo}, nil)
	if err != nil {
		writeJSON(w, 409, response{Success: false, Error: "该订单无法验证仓库，请选择有效的独立履约订单"})
		return
	}
	items := []warehouseOption{}
	for _, v := range objectsWithField(result["info"], "warehouseAddressCode") {
		id := scalarString(v, "warehouseAddressCode")
		name := scalarString(v, "warehouseName", "warehouseAddressName", "warehouseDesc")
		status := scalarString(v, "availableStatus")
		items = append(items, warehouseOption{ID: id, Name: name, CanShip: status == "1" && !shein.IsPGWarehouse(id, name), OMSCode: shein.ResolvedOMSWarehouseCode(id, name)})
	}
	writeJSON(w, 200, response{Success: true, Data: items})
}

func (s *Server) warehouseActivity(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	counts, err := s.store.WarehouseActivity(ctx, s.shopKey)
	if err != nil {
		s.internalError(w, "load warehouse activity", err)
		return
	}
	writeJSON(w, 200, response{Success: true, Data: counts})
}
