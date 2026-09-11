from __future__ import annotations

import unittest

from shein_api_manager.platform_mappings import recipe_key


class PlatformMappingTests(unittest.TestCase):
    def test_recipe_key_is_stable_and_ignores_display_metadata(self) -> None:
        self.assertEqual(
            recipe_key({"items": [{"warehouse_sku": "B", "quantity": 2}, {"warehouse_sku": "A", "quantity": 1, "product_name": "A"}]}),
            (("A", 1), ("B", 2)),
        )


if __name__ == "__main__":
    unittest.main()
