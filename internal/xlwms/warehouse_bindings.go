package xlwms

import (
	"context"
	"net/http"
	"net/url"
)

type WarehouseBinding struct {
	WarehouseKey        string `json:"warehouse_key"`
	OMSCode             string `json:"oms_code"`
	PlatformWarehouseID string `json:"platform_warehouse_id"`
	Effective           bool   `json:"effective"`
	Revision            int64  `json:"revision"`
}

func (c *Client) WarehouseBindings(ctx context.Context, shop string) ([]WarehouseBinding, error) {
	items := []WarehouseBinding{}
	q := url.Values{"platform": {"shein"}, "shop": {shop}}
	err := c.do(ctx, http.MethodGet, "/fulfillment-warehouse-bindings?"+q.Encode(), nil, "", "", "", &items)
	return items, err
}
