from __future__ import annotations

import os
import unittest
from unittest.mock import patch

import pandas as pd
from fastapi.testclient import TestClient

os.environ["SHEIN_WEB_COOKIE_SECURE"] = "false"

from shein_api_manager.pnl_web import app


class ShippingFeeTests(unittest.TestCase):
    def setUp(self) -> None:
        self.client = TestClient(app, follow_redirects=False)
        response = self.client.post("/login", data={"username": "pyy", "password": "12345"})
        self.assertEqual(response.status_code, 303)

    def tearDown(self) -> None:
        self.client.close()

    @staticmethod
    def shipping_rows() -> pd.DataFrame:
        rows = []
        for day_index, day in enumerate(pd.date_range("2026-01-01", "2026-01-06", freq="D")):
            for sku, title, image_url, shein_sku, hit in (
                ("WH-A", "Alpha Shirt", "https://img.example/a.jpg", "S-A", day_index > 0),
                ("WH-B", "Beta Dress", "https://img.example/b.jpg", "S-B", day_index % 2 == 0),
            ):
                if sku == "WH-A" and day == pd.Timestamp("2026-01-04"):
                    continue
                rows.append(
                    {
                        "shipping_fee_day": day,
                        "shipping_fee_hit": hit,
                        "shipping_fee_value_usd": 2.5 if hit else 0.0,
                        "shipping_fee_allocated_usd": 2.5 if hit else 0.0,
                        "order_no": f"ORDER-{day.date()}",
                        "order_created_dt": day + pd.Timedelta(hours=10),
                        "order_status_label": "已签收",
                        "goods_id": f"G-{sku}-{day.date()}",
                        "skc": f"PLATFORM-SKC-{sku}",
                        "skc_name": f"SKC Name {sku}",
                        "goods_title": title,
                        "item_estimated_income": 10.0 if sku == "WH-A" else 20.0,
                        "order_currency": "USD",
                        "allocation_method": "revenue",
                        "allocation_ratio": 0.4 if sku == "WH-A" else 0.6,
                        "base_revenue_allocated_usd": 10.0 if sku == "WH-A" else 20.0,
                        "gross_revenue_allocated_usd": (10.0 if sku == "WH-A" else 20.0) + (2.5 if hit else 0.0),
                        "performance_service_charge_allocated_usd": 2.0 if sku == "WH-A" else 3.0,
                        "order_product_total_price": 33.0,
                        "order_total_service_charge": 3.0,
                        "pnl_product_cost_usd": 1.0 if sku == "WH-A" else 2.0,
                        "pnl_packaging_fee_usd": 0.5,
                        "after_sales_cost_usd": 0.0,
                        "profit_usd": ((10.0 if sku == "WH-A" else 20.0) + (2.5 if hit else 0.0)) - (2.0 if sku == "WH-A" else 3.0) - (1.0 if sku == "WH-A" else 2.0) - 0.5,
                        "profit_margin": ((((10.0 if sku == "WH-A" else 20.0) + (2.5 if hit else 0.0)) - (2.0 if sku == "WH-A" else 3.0) - (1.0 if sku == "WH-A" else 2.0) - 0.5) / ((10.0 if sku == "WH-A" else 20.0) + (2.5 if hit else 0.0))),
                        "pnl_policy": "standard",
                        "warehouse_sku_key": sku,
                        "warehouse_sku_label": sku,
                        "shein_sku_code": shein_sku,
                        "sku_label": title,
                        "skc_key": f"SKC-{sku}",
                        "skc_label": f"SKC-{sku}",
                        "product_image_url": image_url,
                        "product_title": title,
                    }
                )
        return pd.DataFrame(rows)

    def test_min_periods_defaults_to_rolling_and_returns_sku_metadata(self) -> None:
        with patch("shein_api_manager.pnl_web.shipping_fee_effective_items", return_value=self.shipping_rows()):
            response = self.client.get(
                "/api/shipping-fee/data",
                params={
                    "start": "2026-01-03",
                    "end": "2026-01-06",
                    "rolling": 4,
                    "shift": 1,
                    "dimension": "warehouse_sku",
                    "topN": 2,
                },
            )

        self.assertEqual(response.status_code, 200)
        data = response.json()
        self.assertEqual(data["summary"]["minPeriods"], 4)
        self.assertEqual(len(data["groups"]), 2)
        groups = {group["key"]: group for group in data["groups"]}
        self.assertEqual(groups["WH-A"]["title"], "Alpha Shirt")
        self.assertEqual(groups["WH-A"]["imageUrl"], "https://img.example/a.jpg")
        self.assertEqual(groups["WH-A"]["sheinSku"], "S-A")
        alpha_series = [row for row in data["series"] if row["groupKey"] == "WH-A"]
        rates_by_day = {row["day"][:10]: row["rate"] for row in alpha_series}
        self.assertEqual(rates_by_day["2026-01-04"], rates_by_day["2026-01-03"])
        self.assertTrue(all("changePp" not in row for row in alpha_series))

    def test_min_periods_is_clamped_to_rolling_window(self) -> None:
        with patch("shein_api_manager.pnl_web.shipping_fee_effective_items", return_value=self.shipping_rows()):
            response = self.client.get(
                "/api/shipping-fee/data",
                params={
                    "start": "2026-01-01",
                    "end": "2026-01-02",
                    "rolling": 3,
                    "minPeriods": 20,
                    "shift": 1,
                    "dimension": "warehouse_sku",
                },
            )

        self.assertEqual(response.status_code, 200)
        data = response.json()
        self.assertEqual(data["summary"]["minPeriods"], 3)
        self.assertIsNone(data["table"][0]["currentRate"])

    def test_unachieved_orders_return_parent_totals_and_all_order_items(self) -> None:
        with patch("shein_api_manager.pnl_web.shipping_fee_effective_items", return_value=self.shipping_rows()):
            response = self.client.get(
                "/api/shipping-fee/unachieved-orders",
                params={"warehouseSku": "WH-A", "start": "2026-01-01", "end": "2026-01-06"},
            )

        self.assertEqual(response.status_code, 200)
        data = response.json()
        self.assertEqual(data["summary"]["orders"], 1)
        self.assertEqual(data["summary"]["skus"], 2)
        order = data["orders"][0]
        self.assertEqual(order["orderNo"], "ORDER-2026-01-01")
        self.assertEqual(order["orderFulfillmentFeeUsd"], 5.0)
        self.assertEqual(order["orderProductTotalPrice"], 33.0)
        self.assertEqual(order["orderServiceCharge"], 3.0)
        self.assertEqual(order["orderRevenueUsd"], 32.5)
        self.assertEqual(order["orderProfitUsd"], 23.5)
        self.assertEqual(order["orderProfitMargin"], 0.7231)
        self.assertEqual(order["orderShippingFeeUsd"], 2.5)
        self.assertEqual(sum(item["fulfillmentFeeAllocatedUsd"] for item in order["items"]), order["orderFulfillmentFeeUsd"])
        self.assertEqual(sum(item["revenueAllocatedUsd"] for item in order["items"]), order["orderRevenueUsd"])
        target = next(item for item in order["items"] if item["warehouseSku"] == "WH-A")
        self.assertTrue(target["unachievedTarget"])
        self.assertEqual(target["platformSkc"], "PLATFORM-SKC-WH-A")
        self.assertEqual(target["price"], 10.0)

    def test_unachieved_order_page_is_available(self) -> None:
        response = self.client.get(
            "/shipping-fee/orders?warehouseSku=WH-A&start=2026-01-01&end=2026-01-06"
        )

        self.assertEqual(response.status_code, 200)
        self.assertIn("Shipping Fee 未达成订单", response.text)
        self.assertIn("订单总履约费", response.text)
        self.assertIn("订单利润", response.text)


if __name__ == "__main__":
    unittest.main()
