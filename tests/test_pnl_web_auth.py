from __future__ import annotations

import os
import subprocess
import unittest
from unittest.mock import patch

from fastapi.testclient import TestClient

os.environ["SHEIN_WEB_COOKIE_SECURE"] = "false"

from shein_api_manager.pnl_web import (
    ACCESS_COST_TEMPLATES,
    ACCESS_INVENTORY,
    ACCESS_SKU_MAPPINGS,
    COOKIE_NAME,
    ROLE_ADMIN,
    ROLE_OPERATIONS,
    ROLE_ORDER_FOLLOW_UP,
    ROLE_TEST,
    VIEW_LOGISTICS,
    VIEW_PROFIT,
    VIEW_RETURNS,
    VIEW_SHIPPING_FEE,
    VIEW_WAREHOUSE_COST,
    VIEW_WAREHOUSE_RELATIONS,
    app,
    load_accounts,
    warehouse_relation_rows_for_account,
)


class PnlWebAuthTests(unittest.TestCase):
    def setUp(self) -> None:
        self.client = TestClient(app, follow_redirects=False)

    def tearDown(self) -> None:
        self.client.close()

    def login(self, username: str, password: str):
        return self.client.post("/login", data={"username": username, "password": password})

    def test_accounts_have_expected_permissions(self) -> None:
        accounts = load_accounts()
        operations = accounts["operations"]
        follow_up = accounts["order-follow-up"]

        self.assertEqual(accounts["pyy"].role, ROLE_ADMIN)
        self.assertEqual(accounts["temu-test"].role, ROLE_TEST)
        self.assertEqual(operations.role, ROLE_OPERATIONS)
        self.assertEqual(follow_up.role, ROLE_ORDER_FOLLOW_UP)
        self.assertTrue(accounts["pyy"].can(VIEW_PROFIT))
        self.assertFalse(operations.can(VIEW_PROFIT))
        self.assertFalse(follow_up.can(VIEW_PROFIT))
        for permission in (
            VIEW_RETURNS,
            VIEW_LOGISTICS,
            VIEW_SHIPPING_FEE,
            ACCESS_SKU_MAPPINGS,
            VIEW_WAREHOUSE_RELATIONS,
        ):
            self.assertTrue(operations.can(permission))
        for permission in (VIEW_WAREHOUSE_COST, ACCESS_INVENTORY, ACCESS_COST_TEMPLATES):
            self.assertFalse(operations.can(permission))
        for permission in (
            VIEW_RETURNS,
            ACCESS_SKU_MAPPINGS,
            VIEW_WAREHOUSE_RELATIONS,
            VIEW_WAREHOUSE_COST,
            ACCESS_INVENTORY,
            ACCESS_COST_TEMPLATES,
        ):
            self.assertTrue(follow_up.can(permission))
        for permission in (VIEW_LOGISTICS, VIEW_SHIPPING_FEE):
            self.assertFalse(follow_up.can(permission))

    def test_warehouse_relation_cost_filter(self) -> None:
        accounts = load_accounts()
        rows = [
            {
                "skuCode": "sku-1",
                "costText": "USD 2.50",
                "costList": [{"currency": "USD", "cost": 2.5}],
            }
        ]

        operations_rows = warehouse_relation_rows_for_account(rows, accounts["operations"])
        follow_up_rows = warehouse_relation_rows_for_account(rows, accounts["order-follow-up"])

        self.assertEqual(operations_rows, [{"skuCode": "sku-1"}])
        self.assertIn("costText", follow_up_rows[0])
        self.assertIn("costList", follow_up_rows[0])
        self.assertIsNot(operations_rows, rows)
        self.assertIs(follow_up_rows, rows)

    def test_operations_page_access_and_hidden_cost(self) -> None:
        login = self.login("operations", "operations")

        self.assertEqual(login.status_code, 303)
        self.assertEqual(login.headers["location"], "/logistics")
        self.assertEqual(self.client.get("/").status_code, 303)
        for path in ("/logistics", "/shipping-fee", "/returns", "/sku-mappings"):
            self.assertEqual(self.client.get(path).status_code, 200)
        relations = self.client.get("/warehouse-relations")
        self.assertEqual(relations.status_code, 200)
        self.assertNotIn("<div>成本</div>", relations.text)
        self.assertNotIn("detailList('成本'", relations.text)
        self.assertEqual(self.client.get("/inventory").status_code, 403)
        self.assertEqual(self.client.get("/inventory-templates").status_code, 403)
        self.assertEqual(self.client.get("/api/inventory").status_code, 403)

    def test_order_follow_up_page_access(self) -> None:
        login = self.login("order-follow-up", "order-follow-up")

        self.assertEqual(login.status_code, 303)
        self.assertEqual(login.headers["location"], "/sku-mappings")
        self.assertEqual(self.client.get("/").status_code, 303)
        for path in ("/returns", "/sku-mappings", "/warehouse-relations", "/inventory", "/inventory-templates"):
            self.assertEqual(self.client.get(path).status_code, 200)
        relations = self.client.get("/warehouse-relations")
        self.assertIn("<div>成本</div>", relations.text)
        self.assertIn("detailList('成本'", relations.text)
        self.assertEqual(self.client.get("/logistics").status_code, 403)
        self.assertEqual(self.client.get("/shipping-fee").status_code, 403)
        self.assertEqual(self.client.get("/api/logistics/filters").status_code, 403)

    def test_login_page_uses_username_and_password(self) -> None:
        response = self.client.get("/")

        self.assertEqual(response.status_code, 200)
        self.assertIn('name="username"', response.text)
        self.assertIn('name="password"', response.text)
        self.assertNotIn('name="token"', response.text)

    def test_wrong_password_is_rejected(self) -> None:
        response = self.login("pyy", "wrong")

        self.assertEqual(response.status_code, 401)
        self.assertIn("用户名或密码不正确", response.text)
        self.assertNotIn(COOKIE_NAME, response.cookies)

    def test_legacy_token_no_longer_authenticates(self) -> None:
        response = self.client.get("/?token=pyy123")

        self.assertEqual(response.status_code, 200)
        self.assertIn('name="username"', response.text)
        self.assertNotIn(COOKIE_NAME, response.cookies)

    def test_pyy_can_open_profit_pages(self) -> None:
        login = self.login("pyy", "12345")

        self.assertEqual(login.status_code, 303)
        self.assertEqual(login.headers["location"], "/")
        self.assertIn("httponly", login.headers["set-cookie"].lower())
        dashboard = self.client.get("/")
        returns = self.client.get("/returns")
        self.assertEqual(dashboard.status_code, 200)
        self.assertEqual(returns.status_code, 200)
        self.assertIn(">PNL<", dashboard.text)
        self.assertIn("退货明细", dashboard.text)

    def test_test_role_cannot_open_profit_pages_or_apis(self) -> None:
        login = self.login("temu-test", "temu-test")

        self.assertEqual(login.status_code, 303)
        self.assertEqual(login.headers["location"], "/sku-mappings")
        self.assertEqual(self.client.get("/").status_code, 303)
        self.assertEqual(self.client.get("/api/filters").status_code, 403)
        self.assertEqual(self.client.get("/api/data").status_code, 403)
        self.assertEqual(self.client.post("/api/sync-latest-orders").status_code, 403)

        mappings = self.client.get("/sku-mappings")
        returns = self.client.get("/returns")
        self.assertEqual(mappings.status_code, 200)
        self.assertEqual(returns.status_code, 200)
        self.assertNotIn(">PNL<", mappings.text)
        self.assertIn("退货明细", mappings.text)

    def test_pyy_can_run_latest_order_sync(self) -> None:
        self.login("pyy", "12345")
        completed = subprocess.CompletedProcess(
            args=[],
            returncode=0,
            stdout='{"orders":{"ordersSeen":3,"ordersInserted":2,"detailsFetched":3}}',
            stderr="",
        )

        with patch("shein_api_manager.pnl_web.subprocess.run", return_value=completed) as run:
            response = self.client.post("/api/sync-latest-orders")

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["sync"]["orders"]["ordersSeen"], 3)
        command = run.call_args.args[0]
        self.assertEqual(command[-2:], ["--data", "orders"])
        self.assertEqual(run.call_args.kwargs["cwd"], os.path.dirname(os.path.dirname(__file__)))
        self.assertEqual(run.call_args.kwargs["timeout"], 300)

    def test_operations_can_sync_latest_returns(self) -> None:
        self.login("operations", "operations")
        sync_completed = subprocess.CompletedProcess(
            args=[],
            returncode=0,
            stdout='{"returns":{"listReturnsSeen":4,"listReturnsInserted":3}}',
            stderr="",
        )
        export_completed = subprocess.CompletedProcess(
            args=[],
            returncode=0,
            stdout='{"orders":4,"item_rows":7}',
            stderr="",
        )

        with patch(
            "shein_api_manager.pnl_web.subprocess.run",
            side_effect=[sync_completed, export_completed],
        ) as run:
            response = self.client.post("/api/returns/sync-latest")

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["sync"]["returns"]["listReturnsSeen"], 4)
        self.assertEqual(response.json()["export"]["item_rows"], 7)
        self.assertEqual(run.call_args_list[0].args[0][-2:], ["--data", "returns"])
        self.assertTrue(run.call_args_list[1].args[0][-1].endswith("export_orders_profit.py"))

    def test_operations_can_sync_and_rebuild_shipping_fee_data(self) -> None:
        self.login("operations", "operations")
        sync_completed = subprocess.CompletedProcess(
            args=[],
            returncode=0,
            stdout='{"orders":{"ordersSeen":5,"ordersInserted":4}}',
            stderr="",
        )
        export_completed = subprocess.CompletedProcess(
            args=[],
            returncode=0,
            stdout='{"orders":5,"item_rows":8}',
            stderr="",
        )

        with patch(
            "shein_api_manager.pnl_web.subprocess.run",
            side_effect=[sync_completed, export_completed],
        ) as run:
            response = self.client.post("/api/shipping-fee/sync-latest")

        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["sync"]["orders"]["ordersSeen"], 5)
        self.assertEqual(response.json()["export"]["item_rows"], 8)
        self.assertEqual(run.call_count, 2)
        self.assertEqual(run.call_args_list[0].args[0][-2:], ["--data", "orders"])
        self.assertTrue(run.call_args_list[1].args[0][-1].endswith("export_orders_profit.py"))

    def test_page_sync_endpoints_enforce_page_permissions(self) -> None:
        self.login("order-follow-up", "order-follow-up")

        self.assertEqual(self.client.post("/api/logistics/sync-latest").status_code, 403)
        self.assertEqual(self.client.post("/api/shipping-fee/sync-latest").status_code, 403)

    def test_tampered_cookie_is_rejected(self) -> None:
        login = self.login("temu-test", "temu-test")
        cookie = login.cookies[COOKIE_NAME]
        self.client.cookies.set(COOKIE_NAME, cookie + "x")

        response = self.client.get("/api/sku-mappings")
        self.assertEqual(response.status_code, 401)

    def test_logout_clears_session(self) -> None:
        self.login("pyy", "12345")

        response = self.client.get("/logout")
        self.assertEqual(response.status_code, 303)
        self.assertEqual(response.headers["location"], "/")
        self.assertIn("Max-Age=0", response.headers["set-cookie"])


if __name__ == "__main__":
    unittest.main()
