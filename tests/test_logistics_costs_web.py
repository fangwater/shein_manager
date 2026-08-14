from __future__ import annotations

import os
import unittest
from unittest.mock import patch

from fastapi.testclient import TestClient

os.environ["SHEIN_WEB_COOKIE_SECURE"] = "false"

from shein_api_manager.pnl_web import app
from shein_api_manager.xlwms_shein_orders import query_shein_cost_order_page


class LogisticsCostsWebTests(unittest.TestCase):
    def setUp(self) -> None:
        self.client = TestClient(app, follow_redirects=False)

    def tearDown(self) -> None:
        self.client.close()

    def login(self, username: str, password: str) -> None:
        response = self.client.post(
            "/login", data={"username": username, "password": password}
        )
        self.assertEqual(response.status_code, 303)

    def test_logistics_roles_can_open_page(self) -> None:
        self.login("operations", "operations")

        response = self.client.get("/logistics-costs")

        self.assertEqual(response.status_code, 200)
        self.assertIn("物流费用", response.text)
        self.assertIn("同步最新费用", response.text)

    def test_non_logistics_role_cannot_open_page(self) -> None:
        self.login("order-follow-up", "order-follow-up")

        self.assertEqual(self.client.get("/logistics-costs").status_code, 403)
        self.assertEqual(
            self.client.get("/api/logistics-costs/orders").status_code, 403
        )

    @patch("shein_api_manager.pnl_web.database_url", return_value="postgresql://example.invalid/shein")
    @patch("shein_api_manager.pnl_web.query_shein_cost_order_page")
    def test_order_page_api_passes_query_without_aggregation(
        self, query_page, _database_url
    ) -> None:
        query_page.return_value = {
            "rows": [
                {
                    "platform_order_no": "SHEIN-1",
                    "order_no": "OMS-1",
                    "wh_code": "WH-1",
                    "cost_detail_count": 2,
                }
            ],
            "page": 2,
            "pageSize": 25,
            "total": 1,
            "pages": 1,
        }
        self.login("operations", "operations")

        response = self.client.get(
            "/api/logistics-costs/orders?search=SHEIN-1&page=2&pageSize=25"
        )

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["rows"][0]["cost_detail_count"], 2)
        call = query_page.call_args.kwargs
        self.assertEqual(call["search"], "SHEIN-1")
        self.assertEqual(call["page"], 2)
        self.assertEqual(call["page_size"], 25)

    @patch("shein_api_manager.pnl_web.query_cost_order_detail")
    def test_detail_api_preserves_multiple_cost_orders_and_items(
        self, query_detail
    ) -> None:
        query_detail.return_value = {
            "platformOrderNo": "SHEIN-1",
            "relations": [
                {"wh_code": "WH-1", "order_no": "OMS-1"},
                {"wh_code": "WH-2", "order_no": "OMS-2"},
            ],
            "fundsFlows": [{"order_no": "OMS-1"}, {"order_no": "OMS-2"}],
            "costDetails": [
                {"cost_no": "COST-1", "items": [{"bill_item_name": "运费"}]},
                {"cost_no": "COST-2", "items": [{"bill_item_name": "操作费"}]},
            ],
        }
        self.login("operations", "operations")

        response = self.client.get(
            "/api/logistics-costs/detail?orderNo=SHEIN-1"
        )

        self.assertEqual(response.status_code, 200)
        payload = response.json()
        self.assertEqual(len(payload["relations"]), 2)
        self.assertEqual(len(payload["costDetails"]), 2)
        self.assertEqual(payload["costDetails"][1]["items"][0]["bill_item_name"], "操作费")
        query_detail.assert_called_once_with("SHEIN-1")

    @patch("shein_api_manager.pnl_web.get_xlwms_sync_status")
    @patch("shein_api_manager.pnl_web.start_xlwms_sync")
    def test_sync_button_starts_background_sync(self, start_sync, sync_status) -> None:
        start_sync.return_value = True
        sync_status.return_value = {"running": True, "phase": "starting"}
        self.login("operations", "operations")

        response = self.client.post(
            "/api/logistics-costs/sync?detailLimit=500"
        )

        self.assertEqual(response.status_code, 202)
        self.assertTrue(response.json()["running"])
        start_sync.assert_called_once_with(detail_limit=500)

    def test_query_page_rejects_invalid_pagination_before_database(self) -> None:
        with self.assertRaisesRegex(ValueError, "pageSize"):
            query_shein_cost_order_page(
                shein_database_url="", shop_key="", page_size=201
            )


if __name__ == "__main__":
    unittest.main()
