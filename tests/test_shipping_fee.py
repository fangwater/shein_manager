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
                        "order_no": f"{sku}-{day.date()}",
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


if __name__ == "__main__":
    unittest.main()
