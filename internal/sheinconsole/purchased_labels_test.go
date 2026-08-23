package sheinconsole

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPurchasedLabelLookupRejectsInvalidBatchesBeforeStoreAccess(t *testing.T) {
	server := &Server{}
	for _, body := range []string{`{"platform_order_nos":[]}`, `{"platform_order_nos":[""]}`, `{"unknown":[]}`} {
		request := httptest.NewRequest(http.MethodPost, "/api/label-purchases/lookup", strings.NewReader(body))
		response := httptest.NewRecorder()
		server.purchasedLabelLookup(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s returned %d: %s", body, response.Code, response.Body.String())
		}
	}
}
