package sheinconsole

import (
	"testing"
	"time"

	"shein-api-manager/internal/shein"
	"shein-api-manager/internal/xlwms"
)

func TestApplyLingxingParcelWarehouseDecisionUsesOutboundStatus(t *testing.T) {
	status := 2
	update := shein.ParcelWatchUpdate{}
	if !applyLingxingParcelWarehouseDecision(&update, lingxingParcelStatus{
		OutboundOrderNo: "OBS5272608180TK", Status: &status, StatusName: "仓库处理中", LabelAttached: true,
	}) {
		t.Fatal("labeled warehouse-processing parcel must complete warehouse detection")
	}
	if update.OMSSyncStatus != "verified" || update.OMSStatusCode == nil || *update.OMSStatusCode != 2 {
		t.Fatalf("update = %#v", update)
	}
	canceled := 4
	update = shein.ParcelWatchUpdate{}
	if !applyLingxingParcelWarehouseDecision(&update, lingxingParcelStatus{
		OutboundOrderNo: "OBS-CANCELED", Status: &canceled, StatusName: "已取消",
	}) || update.OMSSyncStatus != "manual_required" {
		t.Fatalf("canceled parcel = %#v", update)
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
	decision := decideSHEINOMSPlatformOrder(processing, xlwms.PlatformOrderLookup{Account: "arp"}, "DPSNY002", now.Add(-time.Minute), now, "")
	if !decision.Verified || decision.State != "processing" || decision.Target.OMSOrderNo != "OMS-1" {
		t.Fatalf("processing decision = %#v", decision)
	}

	missing := decideSHEINOMSPlatformOrder(xlwms.PlatformOrderLookup{Account: "dps"}, xlwms.PlatformOrderLookup{Account: "arp"}, "DPSNY002", now.Add(-time.Minute), now, "")
	if missing.ManualRequired || missing.State != "missing" {
		t.Fatalf("grace-period missing decision = %#v", missing)
	}
	expired := decideSHEINOMSPlatformOrder(xlwms.PlatformOrderLookup{Account: "dps"}, xlwms.PlatformOrderLookup{Account: "arp"}, "DPSNY002", now.Add(-31*time.Minute), now, "")
	if !expired.ManualRequired {
		t.Fatalf("expired missing decision = %#v", expired)
	}

	collision := decideSHEINOMSPlatformOrder(processing, xlwms.PlatformOrderLookup{
		Account: "arp", Orders: []xlwms.PlatformOrder{{Status: 3, StatusText: "已发货"}},
	}, "DPSNY002", now, now, "")
	if !collision.ManualRequired {
		t.Fatalf("cross-account collision was accepted: %#v", collision)
	}
}

func TestDecideSHEINOMSPlatformOrderArchivesSelfCompletedSHEINOrders(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	empty := xlwms.PlatformOrderLookup{Account: "dps"}
	canceledOpposite := xlwms.PlatformOrderLookup{
		Account: "arp",
		Orders: []xlwms.PlatformOrder{{
			OMSOrderNo: "SO389260807000474", Status: 4, StatusKey: "canceled", StatusText: "已取消",
		}},
	}

	archived, ok := sheinPlatformAlreadyFulfilledDecision("5")
	if !ok || !archived.Verified || archived.State != "delivered" {
		t.Fatalf("delivered SHEIN order was not archived: %#v ok=%v", archived, ok)
	}

	decision := decideSHEINOMSPlatformOrder(empty, canceledOpposite, "DPSCA004", now.Add(-24*time.Hour), now, "delivered")
	if !decision.Verified || decision.ManualRequired || decision.Target.OMSOrderNo != "SO389260807000474" {
		t.Fatalf("self-completed SHEIN order stayed in waiting/leak: %#v", decision)
	}

	canceledExpected := xlwms.PlatformOrderLookup{
		Account: "dps", Found: true, MatchCount: 1,
		Orders: []xlwms.PlatformOrder{{
			OMSOrderNo: "SO-DPS", Status: 4, StatusKey: "canceled", StatusText: "已取消", SendWarehouseCode: "DPSCA004",
		}},
	}
	canceled := decideSHEINOMSPlatformOrder(canceledExpected, xlwms.PlatformOrderLookup{Account: "arp"}, "DPSCA004", now, now, "shipped")
	if !canceled.Verified || canceled.ManualRequired {
		t.Fatalf("shipped SHEIN order with canceled OMS was not archived: %#v", canceled)
	}

	stillWaiting := decideSHEINOMSPlatformOrder(empty, canceledOpposite, "DPSCA004", now.Add(-time.Minute), now, "pending_shipping")
	if stillWaiting.Verified || stillWaiting.ManualRequired || stillWaiting.State != "missing" {
		t.Fatalf("open SHEIN order was archived too early: %#v", stillWaiting)
	}
}
