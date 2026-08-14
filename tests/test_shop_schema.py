from __future__ import annotations

import unittest

from psycopg.conninfo import conninfo_to_dict

from shein_api_manager.db import (
    REGISTRY_SCHEMA_SQL,
    SHOP_SCHEMA_SQL,
    schema_database_url,
)


class ShopSchemaTests(unittest.TestCase):
    def test_shop_connection_uses_private_schema_then_public(self) -> None:
        conninfo = schema_database_url(
            "postgresql://user:secret@localhost/shein",
            "shein_beauty_hangers_home",
        )
        parsed = conninfo_to_dict(conninfo)
        self.assertEqual(
            parsed["options"],
            "-csearch_path=shein_beauty_hangers_home,public",
        )

    def test_shop_connection_rejects_unsafe_schema(self) -> None:
        with self.assertRaisesRegex(ValueError, "invalid PostgreSQL shop schema"):
            schema_database_url("postgresql://localhost/shein", "public;drop schema public")

    def test_registry_and_private_ddl_have_separate_ownership(self) -> None:
        self.assertIn("CREATE TABLE IF NOT EXISTS public.shein_shops", REGISTRY_SCHEMA_SQL)
        self.assertNotIn("CREATE TABLE IF NOT EXISTS shein_shops", SHOP_SCHEMA_SQL)
        self.assertIn("REFERENCES public.shein_shops", SHOP_SCHEMA_SQL)
        self.assertIn("ON UPDATE CASCADE", SHOP_SCHEMA_SQL)


if __name__ == "__main__":
    unittest.main()
