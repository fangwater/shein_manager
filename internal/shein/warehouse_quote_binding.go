package shein

import (
	"context"
	"errors"
)

func (s *Store) ValidateShippingQuoteBinding(ctx context.Context, shop, id, oms string, revision int64) error {
	var code string
	var saved int64
	if err := s.pool.QueryRow(ctx, `SELECT oms_warehouse_code,binding_revision FROM shein_go_shipping_quotes WHERE shop_key=$1 AND pre_request_id=$2`, shop, id).Scan(&code, &saved); err != nil {
		return err
	}
	if (code != "" && code != oms) || (saved > 0 && saved != revision) {
		return errors.New("报价后的仓库映射已变化，请重新查询物流渠道")
	}
	return nil
}

// Import historical physical identities once; future management changes never overwrite them.
func (s *Store) backfillLegacyWarehouseSnapshots(ctx context.Context) error {
	for address, code := range LegacyWarehouseMappingsForShop(s.shopKey) {
		if _, err := s.pool.Exec(ctx, `UPDATE shein_go_shipping_quotes SET oms_warehouse_code=$3 WHERE shop_key=$1 AND warehouse_address_code=$2 AND oms_warehouse_code=''`, s.shopKey, address, code); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `UPDATE shein_label_purchase_choices SET oms_warehouse_code=$3 WHERE shop_key=$1 AND selected_warehouse_address_code=$2 AND oms_warehouse_code=''`, s.shopKey, address, code); err != nil {
			return err
		}
	}
	return nil
}
