from __future__ import annotations

import os
import subprocess
import unittest
from unittest.mock import patch

from fastapi.testclient import TestClient

os.environ["SHEIN_WEB_COOKIE_SECURE"] = "false"

from shein_api_manager.pnl_web import (
    COOKIE_NAME,
    ROLE_ADMIN,
    ROLE_OPERATIONS,
    ROLE_ORDER_FOLLOW_UP,
    ROLE_TEST,
    VIEW_PROFIT,
    app,
    load_accounts,
)


class PnlWebAuthTests(unittest.TestCase):
    def setUp(self) -> None:
        self.client = TestClient(app, follow_redirects=False)

    def tearDown(self) -> None:
        self.client.close()

    def login(self, username: str, password: str):
        return self.client.post("/login", data={"username": username, "password": password})

    def test_accounts_have_expected_roles(self) -> None:
        accounts = load_accounts()

        self.assertEqual(accounts["pyy"].role, ROLE_ADMIN)
        self.assertEqual(accounts["temu-test"].role, ROLE_TEST)
        self.assertEqual(accounts["operations"].role, ROLE_OPERATIONS)
        self.assertEqual(accounts["order-follow-up"].role, ROLE_ORDER_FOLLOW_UP)
        self.assertTrue(accounts["pyy"].can(VIEW_PROFIT))
        self.assertFalse(accounts["temu-test"].can(VIEW_PROFIT))
        self.assertFalse(accounts["operations"].can(VIEW_PROFIT))
        self.assertFalse(accounts["order-follow-up"].can(VIEW_PROFIT))

    def test_new_role_accounts_can_login(self) -> None:
        operations = self.login("operations", "operations")

        self.assertEqual(operations.status_code, 303)
        self.assertEqual(operations.headers["location"], "/logistics")
        self.assertEqual(self.client.get("/returns").status_code, 403)

        self.client.get("/logout")
        follow_up = self.login("order-follow-up", "order-follow-up")
        self.assertEqual(follow_up.status_code, 303)
        self.assertEqual(follow_up.headers["location"], "/sku-mappings")
        self.assertEqual(self.client.get("/returns").status_code, 403)

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
        self.assertEqual(self.client.get("/returns").status_code, 403)
        self.assertEqual(self.client.get("/api/filters").status_code, 403)
        self.assertEqual(self.client.get("/api/data").status_code, 403)
        self.assertEqual(self.client.get("/api/returns/data").status_code, 403)
        self.assertEqual(self.client.post("/api/sync-latest-orders").status_code, 403)

        mappings = self.client.get("/sku-mappings")
        self.assertEqual(mappings.status_code, 200)
        self.assertNotIn(">PNL<", mappings.text)
        self.assertNotIn("退货明细", mappings.text)

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
