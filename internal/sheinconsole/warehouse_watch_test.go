package sheinconsole

import (
	"fmt"
	"testing"
	"time"

	"shein-api-manager/internal/shein"
	"shein-api-manager/internal/xlwms"
)

func TestExpiredTrackingPackageErrorIsNotRetryable(t *testing.T) {
	expired := fmt.Errorf("attach label: %w", &shein.APIError{Code: "9999400", Message: "expired"})
	if !isExpiredTrackingPackageError(expired) {
		t.Fatal("expired tracking package error must be classified as non-retryable")
	}
	if isExpiredTrackingPackageError(&shein.APIError{Code: "9999401", Message: "another error"}) {
		t.Fatal("unrelated SHEIN error must remain retryable")
	}
	if isExpiredTrackingPackageError(fmt.Errorf("network failure")) {
		t.Fatal("transport error must remain retryable")
	}
}

func TestTaskNeedsManualResolutionSkipsWarehouseWatch(t *testing.T) {
	if !taskNeedsManualResolution(shein.FulfillmentTask{OMSSyncStatus: "manual_required"}) {
		t.Fatal("unassigned manual-required task must not be watched automatically")
	}
	if taskNeedsManualResolution(shein.FulfillmentTask{OMSSyncStatus: "manual_required", OutboundOrderNo: "OBS-1"}) {
		t.Fatal("task with an outbound order must remain available for warehouse status updates")
	}
	if taskNeedsManualResolution(shein.FulfillmentTask{OMSSyncStatus: "waiting_sync"}) {
		t.Fatal("waiting task must remain available for warehouse status updates")
	}
}

func TestApplyLingxingParcelWarehouseDecisionUsesOutboundStatus(t *testing.T) {
	status := 2
	update := shein.ParcelWatchUpdate{}
	if !applyLingxingParcelWarehouseDecision(&update, lingxingParcelStatus{
		OutboundOrderNo: "OBS5272608180TK", Status: &status, StatusName: "仓库处理中", LabelAttached: true,
	}) {
		t.Fatal("labeled warehouse-processing parcel must complete warehouse detection")
	}
	if update.OMSSyncStatus != "waiting_sync" || update.OMSStatusCode == nil || *update.OMSStatusCode != 2 || update.OMSStatusKey != "processing" {
		t.Fatalf("warehouse-processing parcel must stay in status 2 until outbound: %#v", update)
	}
	canceled := 4
	update = shein.ParcelWatchUpdate{}
	if !applyLingxingParcelWarehouseDecision(&update, lingxingParcelStatus{
		OutboundOrderNo: "OBS-CANCELED", Status: &canceled, StatusName: "已取消",
	}) || update.OMSSyncStatus != "manual_required" {
		t.Fatalf("canceled parcel = %#v", update)
	}
}

func TestDPSParcelsDoNotUseOMSPlatformLeak(t *testing.T) {
	if shein.RequiresManualParcelCreate("beauty-hangers-home", "WH2604283535967233", "DPSNY002") == false {
		t.Fatal("Beauty Hangers DPS orders must stay on the Lingxing parcel path")
	}
	if shein.RequiresManualParcelCreate("beauty-hangers-home", "WH2607084039788546", "ARP") {
		t.Fatal("ARP orders must still use the OMS platform-order path")
	}
}

func TestAssignPendingOMSPlatformOrderUsesPurchasedWarehouse(t *testing.T) {
	server := &Server{xlwms: nil}
	err := server.assignPendingOMSPlatformOrder(nil, shein.FulfillmentTask{OrderNo: "GSU-1", DeliveryNo: "GU-1"}, shein.LabelPurchaseRecord{DeliveryNo: "GU-1"}, "arp", shein.PurchasedWarehouse{OMSCode: "HYTX30"}, xlwms.PlatformOrderLookup{
		Orders: []xlwms.PlatformOrder{{OMSOrderNo: "SO-1", Status: 0}},
	})
	if err == nil || err.Error() != "领星查询服务未配置" {
		t.Fatalf("missing XLWMS must fail assignment: %v", err)
	}
	if err := (&Server{}).assignPendingOMSPlatformOrder(nil, shein.FulfillmentTask{OrderNo: "GSU-1"}, shein.LabelPurchaseRecord{}, "", shein.PurchasedWarehouse{}, xlwms.PlatformOrderLookup{}); err == nil {
		t.Fatal("empty purchase warehouse must not assign")
	}
}

func TestActiveOMSPlatformOrdersExcludesCanceled(t *testing.T) {
	if len(activeOMSPlatformOrders(xlwms.PlatformOrderLookup{
		Orders: []xlwms.PlatformOrder{{Status: 4}, {Status: 0}},
	})) != 1 {
		t.Fatal("canceled OMS orders must not count as active")
	}
}

func TestDecideSHEINOMSPlatformOrderMatchesTemuWarehouseRules(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	processing := xlwms.PlatformOrderLookup{
		Account: "dps", Found: true, MatchCount: 1,
		Orders: []xlwms.PlatformOrder{{
			OMSOrderNo: "OMS-1", PlatformOrderNo: "GSU-1", Status: 2,
			StatusKey: "processing", StatusText: "处理中", SendWarehouseCode: "DPSNY002",
		}},
	}
	decision := decideSHEINOMSPlatformOrder(processing, "DPSNY002", now.Add(-time.Minute), now, "")
	if decision.Verified || decision.ManualRequired || decision.State != "processing" || decision.Target.OMSOrderNo != "OMS-1" {
		t.Fatalf("processing decision = %#v", decision)
	}

	missing := decideSHEINOMSPlatformOrder(xlwms.PlatformOrderLookup{Account: "dps"}, "DPSNY002", now.Add(-time.Minute), now, "")
	if missing.ManualRequired || missing.State != "missing" {
		t.Fatalf("grace-period missing decision = %#v", missing)
	}
	expired := decideSHEINOMSPlatformOrder(xlwms.PlatformOrderLookup{Account: "dps"}, "DPSNY002", now.Add(-31*time.Minute), now, "")
	if !expired.ManualRequired {
		t.Fatalf("expired missing decision = %#v", expired)
	}

	mismatch := decideSHEINOMSPlatformOrder(xlwms.PlatformOrderLookup{
		Account: "dps", Found: true, MatchCount: 1,
		Orders: []xlwms.PlatformOrder{{
			OMSOrderNo: "OMS-2", PlatformOrderNo: "GSU-2", Status: 2,
			StatusKey: "processing", StatusText: "处理中", SendWarehouseCode: "HYTX30",
		}},
	}, "DPSNY002", now, now, "")
	if !mismatch.ManualRequired || mismatch.Message != "领星仓库不一致" {
		t.Fatalf("label/OMS warehouse mismatch was accepted: %#v", mismatch)
	}
}

func TestDecideSHEINOMSPlatformOrderArchivesSelfCompletedSHEINOrders(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	empty := xlwms.PlatformOrderLookup{Account: "dps"}
	archived, ok := sheinPlatformAlreadyFulfilledDecision("5")
	if !ok || !archived.Verified || archived.State != "delivered" {
		t.Fatalf("delivered SHEIN order was not archived: %#v ok=%v", archived, ok)
	}

	decision := decideSHEINOMSPlatformOrder(empty, "DPSCA004", now.Add(-24*time.Hour), now, "delivered")
	if !decision.Verified || decision.ManualRequired {
		t.Fatalf("self-completed SHEIN order stayed in waiting/leak: %#v", decision)
	}

	canceledExpected := xlwms.PlatformOrderLookup{
		Account: "dps", Found: true, MatchCount: 1,
		Orders: []xlwms.PlatformOrder{{
			OMSOrderNo: "SO-DPS", Status: 4, StatusKey: "canceled", StatusText: "已取消", SendWarehouseCode: "DPSCA004",
		}},
	}
	canceled := decideSHEINOMSPlatformOrder(canceledExpected, "DPSCA004", now, now, "shipped")
	if !canceled.Verified || canceled.ManualRequired {
		t.Fatalf("shipped SHEIN order with canceled OMS was not archived: %#v", canceled)
	}

	stillWaiting := decideSHEINOMSPlatformOrder(empty, "DPSCA004", now.Add(-time.Minute), now, "pending_shipping")
	if stillWaiting.Verified || stillWaiting.ManualRequired || stillWaiting.State != "missing" {
		t.Fatalf("open SHEIN order was archived too early: %#v", stillWaiting)
	}
}
