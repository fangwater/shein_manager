from __future__ import annotations

import os
import unittest

from fastapi.testclient import TestClient

os.environ["SHEIN_WEB_COOKIE_SECURE"] = "false"

from shein_api_manager.pnl_web import COOKIE_NAME, app


class PnlWebAuthTests(unittest.TestCase):
    def setUp(self) -> None:
        self.client = TestClient(app, follow_redirects=False)

    def tearDown(self) -> None:
        self.client.close()

    def login(self, username: str, password: str):
        return self.client.post("/login", data={"username": username, "password": password})

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

    def test_admin_cannot_open_profit_pages_or_apis(self) -> None:
        login = self.login("temu-test", "temu-test")

        self.assertEqual(login.status_code, 303)
        self.assertEqual(login.headers["location"], "/sku-mappings")
        self.assertEqual(self.client.get("/").status_code, 303)
        self.assertEqual(self.client.get("/returns").status_code, 403)
        self.assertEqual(self.client.get("/api/filters").status_code, 403)
        self.assertEqual(self.client.get("/api/data").status_code, 403)
        self.assertEqual(self.client.get("/api/returns/data").status_code, 403)

        mappings = self.client.get("/sku-mappings")
        self.assertEqual(mappings.status_code, 200)
        self.assertNotIn(">PNL<", mappings.text)
        self.assertNotIn("退货明细", mappings.text)

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
