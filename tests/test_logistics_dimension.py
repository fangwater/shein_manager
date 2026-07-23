from __future__ import annotations

import unittest

import pandas as pd

from shein_api_manager.pnl_web import (
    LOGISTICS_SKU_DIMENSION_PLATFORM,
    LOGISTICS_SKU_DIMENSION_WAREHOUSE,
    filter_logistics_items,
    normalize_logistics_sku_dimension,
    prepare_logistics_items,
)


class LogisticsDimensionTests(unittest.TestCase):
    def items(self) -> pd.DataFrame:
        common = {
            "order_created_dt": pd.Timestamp("2026-07-01 10:00:00"),
            "order_status": "5",
            "order_status_label": "已发货",
            "order_status_normalized": "shipped",
            "return_detected": False,
            "after_sales_status": False,
            "pnl_policy": "standard",
            "goods_weight": 0.25,
        }
        return pd.DataFrame([
            {
                **common,
                "goods_id": "g1",
                "order_no": "o1",
                "sku_code": "P-RED",
                "shein_sku_code": "P-RED",
                "sku_label": "红色",
                "warehouse_sku_label": "WH-A",
                "warehouse_sku_key": "WH-A",
            },
            {
                **common,
                "goods_id": "g2",
                "order_no": "o2",
                "sku_code": "P-BLUE",
                "shein_sku_code": "P-BLUE",
                "sku_label": "蓝色",
                "warehouse_sku_label": "WH-A",
                "warehouse_sku_key": "WH-A",
            },
            {
                **common,
                "goods_id": "g3",
                "order_no": "o3",
                "sku_code": "P-GREEN",
                "shein_sku_code": "P-GREEN",
                "sku_label": "绿色",
                "warehouse_sku_label": "WH-B",
                "warehouse_sku_key": "WH-B",
            },
        ])

    def test_platform_and_warehouse_dimensions_group_differently(self) -> None:
        platform = prepare_logistics_items(self.items(), LOGISTICS_SKU_DIMENSION_PLATFORM)
        warehouse = prepare_logistics_items(self.items(), LOGISTICS_SKU_DIMENSION_WAREHOUSE)

        self.assertEqual(platform["logistics_sku_key"].nunique(), 3)
        self.assertEqual(warehouse["logistics_sku_key"].nunique(), 2)
        self.assertEqual(set(warehouse["logistics_sku_code"]), {"WH-A", "WH-B"})
        self.assertEqual(warehouse.loc[warehouse["logistics_sku_key"].eq("WH-A"), "logistics_platform_sku_code"].nunique(), 2)

    def test_filter_uses_keys_from_selected_dimension(self) -> None:
        warehouse = prepare_logistics_items(self.items(), LOGISTICS_SKU_DIMENSION_WAREHOUSE)

        filtered = filter_logistics_items(warehouse, sku_keys=["WH-A"], start=None, end=None)

        self.assertEqual(set(filtered["sku_code"]), {"P-RED", "P-BLUE"})

    def test_unknown_dimension_defaults_to_platform(self) -> None:
        self.assertEqual(normalize_logistics_sku_dimension("unknown"), LOGISTICS_SKU_DIMENSION_PLATFORM)


if __name__ == "__main__":
    unittest.main()
