package sheinconsole

import (
	"shein-api-manager/internal/shein"
	"testing"
)

func TestParcelUsesPurchasedSnapshotForNewPlatformAddress(t *testing.T) {
	task := shein.FulfillmentTask{OrderNo: "test-order", WarehouseAddressCode: "WH-NEW-DPS", OMSWarehouseCode: "DPSCA004", Status: "label_ready", DeliveryNo: "test-delivery"}
	draft := parcelDraftFromTask("beauty-hangers-home", "Beauty Hangers home", task)
	if !draft.Required || draft.Warehouse != "DPSCA004" {
		t.Fatalf("new platform address lost purchased physical warehouse: %#v", draft)
	}
	request := xlwmsParcelCreateRequest{Warehouse: "DPSNY002", ChannelCode: "Upload_Shipping_Label", Receiver: "test", CountryRegionCode: "US", ProvinceName: "CA", CityName: "LA", PostCode: "90001", AddressOne: "test", Products: []xlwmsParcelDraftProduct{{SKU: "test", Quantity: 1}}}
	if _, err := parcelCreateOrder("beauty-hangers-home", "Beauty Hangers home", task.OrderNo, task, request); err == nil || err.Error() != "建单仓库必须与 DPS 发货仓一致" {
		t.Fatalf("wrong physical warehouse was accepted: %v", err)
	}
	request.Warehouse = "DPSCA004"
	if _, err := parcelCreateOrder("beauty-hangers-home", "Beauty Hangers home", task.OrderNo, task, request); err != nil {
		t.Fatal(err)
	}
}
