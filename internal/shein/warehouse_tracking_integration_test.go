package shein

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestNewWarehousePurchaseRemainsTrackable(t *testing.T) {
	databaseURL := os.Getenv("SHEIN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SHEIN_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	schema := fmt.Sprintf("warehouse_tracking_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
	store, err := NewStoreForShop(ctx, databaseURL, schema, "beauty-hangers-home")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Base platform tables are normally created by the shop bootstrap migration.
	if _, err := store.pool.Exec(ctx, `CREATE TABLE shein_orders(shop_key text, detail_payload jsonb); CREATE TABLE shein_product_details(shop_key text, skc_list jsonb)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ shop, address, code string }{
		{"beauty-hangers-home", "WH-NEW-DPS", "DPSCA004"},
		{"other-shop", "WH-NEW-DPS", "ARP06A"},
		{"beauty-hangers-home", "WH-NEW-HOUSTON", "ARP06A"},
	} {
		if _, err := store.pool.Exec(ctx, `INSERT INTO shein_go_shipping_quotes(shop_key,pre_request_id,order_no,warehouse_address_code,oms_warehouse_code) VALUES($1,$2,'test-order',$2,$3)`, fixture.shop, fixture.address, fixture.code); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `INSERT INTO shein_label_purchase_choices(shop_key,pre_request_id,operation_idempotency_key,order_no,selection_source,selected_warehouse_address_code,selected_express_channel_code,selected_performance_cost,selection_reason,delivery_no,oms_warehouse_code) VALUES($1,$2,'test-operation','test-order','manual',$2,'USPS',1,'test',$2,$3)`, fixture.shop, fixture.address, fixture.code); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `INSERT INTO shein_go_fulfillment_tasks(shop_key,order_no,warehouse_address_code,delivery_no,status) VALUES($1,'test-order',$2,$2,'label_ready')`, fixture.shop, fixture.address); err != nil {
			t.Fatal(err)
		}
	}
	// An unpurchased unknown warehouse cannot borrow another task's snapshot.
	if _, err := store.pool.Exec(ctx, `INSERT INTO shein_go_fulfillment_tasks(shop_key,order_no,warehouse_address_code,delivery_no,status) VALUES('beauty-hangers-home','test-order','WH-UNBOUGHT','test-unbought','label_ready')`); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListWarehouseWatchTasks(ctx, "beauty-hangers-home", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("watch tasks = %d, want 2", len(tasks))
	}
	for _, task := range tasks {
		expected := "ARP06A"
		if task.WarehouseAddressCode == "WH-NEW-DPS" {
			expected = "DPSCA004"
		}
		if task.OMSWarehouseCode != expected {
			t.Fatalf("physical identity leaked or lost: %#v", task)
		}
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err = store.ListWarehouseWatchTasks(ctx, "beauty-hangers-home", 100)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("restart lost bought warehouse: %d, %v", len(tasks), err)
	}
}
