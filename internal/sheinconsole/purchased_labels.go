package sheinconsole

import (
	"context"
	"net/http"
	"strings"
)

type purchasedLabelLookupRequest struct {
	PlatformOrderNos []string `json:"platform_order_nos"`
}

func (s *Server) purchasedLabelLookup(writer http.ResponseWriter, request *http.Request) {
	var payload purchasedLabelLookupRequest
	if !decodeJSON(writer, request, &payload) {
		return
	}
	if len(payload.PlatformOrderNos) == 0 || len(payload.PlatformOrderNos) > 50 {
		writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "platform_order_nos must contain 1 to 50 orders"})
		return
	}
	for _, orderNo := range payload.PlatformOrderNos {
		if strings.TrimSpace(orderNo) == "" {
			writeJSON(writer, http.StatusBadRequest, response{Success: false, Error: "platform order number is required"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(request.Context(), s.requestTimeout)
	defer cancel()
	items, err := s.store.PurchasedLabelEvidenceByOrderNos(ctx, s.shopKey, payload.PlatformOrderNos)
	if err != nil {
		s.internalError(writer, "lookup SHEIN purchased labels", err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, response{Success: true, Data: items})
}
