package sheinconsole

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"shein-api-manager/internal/shein"
)

func TestRejectDisabledCarrierPurchaseUsesWarehousePolicy(t *testing.T) {
	policies := []shein.CarrierPolicy{{WarehouseKey: "DPS002", CarrierCode: "GOFO", Priority: 1, Enabled: false}}
	reason := shein.ChannelUnavailableReason(
		"GOFO-D2D250718-Na", "", "", "WH2604283535967233", "",
		shein.PoliciesByWarehouse([]shein.WarehouseCarrierPolicies{{
			WarehouseKey: "DPS002", Carriers: policies,
		}})["DPS002"],
	)
	if reason != "SHEIN 发货策略已在 DPS002 仓库禁用 GOFO" {
		t.Fatalf("purchase of a disabled warehouse carrier must be rejected: %q", reason)
	}
}

func TestRequiredConfirmationRecognizesJSONNumber(t *testing.T) {
	confirmation, idempotent, err := requiredConfirmation("export-address", map[string]any{
		"handleType": json.Number("2"),
	})
	if err != nil {
		t.Fatalf("requiredConfirmation returned an error: %v", err)
	}
	if confirmation != "export-address-transition" || !idempotent {
		t.Fatalf("confirmation = %q, idempotent = %v", confirmation, idempotent)
	}
}

func TestCacheableOperationResponseRedactsAddress(t *testing.T) {
	result := map[string]any{
		"code": "0",
		"msg":  "OK",
		"info": map[string]any{
			"receiveMsgList": []any{map[string]any{"address": "secret customer address"}},
		},
	}
	stored := cacheableOperationResponse("export-address", result)
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal stored response: %v", err)
	}
	if strings.Contains(string(encoded), "secret customer address") {
		t.Fatalf("cached response contains address data: %s", encoded)
	}
	info, ok := stored["info"].(map[string]any)
	if !ok || info["redacted"] != true {
		t.Fatalf("cached response is not marked redacted: %#v", stored)
	}
}

func TestOperationErrorSummaryOmitsAPIMessage(t *testing.T) {
	err := &shein.APIError{Code: "1001", Message: "contains order data", TraceID: "trace-1"}
	summary := operationErrorSummary(err)
	if strings.Contains(summary, err.Message) {
		t.Fatalf("operation error summary leaked API message: %q", summary)
	}
	if !strings.Contains(summary, err.Code) || !strings.Contains(summary, err.TraceID) {
		t.Fatalf("operation error summary omitted identifiers: %q", summary)
	}
	if operationErrorSummary(errors.New("database detail")) != "operation failed" {
		t.Fatal("non-API error detail must not be persisted")
	}
}
