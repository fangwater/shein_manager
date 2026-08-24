package sheinconsole

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"shein-api-manager/internal/shein"
)

type fulfillmentTaskResolutionRequest struct {
	PlaceRequestID   string `json:"place_request_id"`
	DeliveryNo       string `json:"delivery_no"`
	PackageNo        string `json:"package_no"`
	FinalStatus      string `json:"final_status"`
	ResolutionReason string `json:"reason"`
}

func (s *Server) resolveFulfillmentTask(writer http.ResponseWriter, request *http.Request) {
	orderNo := strings.TrimSpace(request.PathValue("orderNo"))
	if orderNo == "" || len(orderNo) > 100 {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "订单号无效"})
		return
	}
	var input fulfillmentTaskResolutionRequest
	if !decodeJSON(writer, request, &input) {
		return
	}
	shopKey, err := s.requestedShopKey(request, "")
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	err = s.store.ResolveFulfillmentTask(
		ctx, shopKey, orderNo, input.PlaceRequestID, input.DeliveryNo, input.PackageNo,
		input.FinalStatus, input.ResolutionReason,
	)
	if err != nil {
		if errors.Is(err, shein.ErrFulfillmentTaskNotFound) {
			writeJSON(writer, http.StatusNotFound, response{Success: false, Error: "当前店铺中未找到该履约任务"})
			return
		}
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, response{Success: true, Data: map[string]string{
		"order_no":     orderNo,
		"final_status": strings.TrimSpace(input.FinalStatus),
		"reason":       strings.TrimSpace(input.ResolutionReason),
	}})
}
