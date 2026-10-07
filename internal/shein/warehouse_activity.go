package shein

import "context"

func (s *Store) WarehouseActivity(ctx context.Context, shop string) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.oms_warehouse_code,p.selected_warehouse_address_code,count(*) FROM shein_label_purchase_choices p JOIN shein_go_api_operations o ON o.shop_key=p.shop_key AND o.idempotency_key=p.operation_idempotency_key AND o.operation='place-express-order' WHERE p.shop_key=$1 AND o.status='pending' GROUP BY p.oms_warehouse_code,p.selected_warehouse_address_code`, shop)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var code, address string
		var count int
		if err = rows.Scan(&code, &address, &count); err != nil {
			return nil, err
		}
		if code == "" {
			code = ResolvedOMSWarehouseCode(address, "")
		}
		key := PolicyWarehouseKey(code, "")
		if key != "" {
			counts[key] += count
		}
	}
	return counts, rows.Err()
}
