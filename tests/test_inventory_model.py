from __future__ import annotations

import os
import unittest

from fastapi import HTTPException

os.environ["SHEIN_WEB_COOKIE_SECURE"] = "false"

from shein_api_manager.db import INVENTORY_SCHEMA_SQL
from shein_api_manager.pnl_web import (
    inventory_cost_totals,
    inventory_currency,
    normalize_inventory_costs,
    normalize_inventory_lines,
)


class FakeResult:
    def __init__(self, rows):
        self.rows = rows

    def fetchall(self):
        return self.rows


class FakeConnection:
    def __init__(self, modes):
        self.modes = modes

    def execute(self, query, params):
        self.query = query
        self.params = params
        return FakeResult(
            [{"id": cost_type_id, "calculation_mode": mode} for cost_type_id, mode in self.modes.items()]
        )


class InventoryModelTests(unittest.TestCase):
    def test_schema_uses_inventory_ticket_names_without_shein_prefix(self) -> None:
        self.assertIn("CREATE TABLE IF NOT EXISTS inventory_tickets", INVENTORY_SCHEMA_SQL)
        self.assertIn("CREATE TABLE IF NOT EXISTS inventory_cost_templates", INVENTORY_SCHEMA_SQL)
        self.assertIn("cost_template_id bigint", INVENTORY_SCHEMA_SQL)
        self.assertIn("inventory_ticket_id bigint", INVENTORY_SCHEMA_SQL)
        self.assertNotIn("CREATE TABLE IF NOT EXISTS shein_inventory_tickets", INVENTORY_SCHEMA_SQL)

    def test_inventory_lines_support_multiple_skus_and_calculate_amount(self) -> None:
        lines = normalize_inventory_lines(
            [
                {"warehouseSku": "WH-1", "quantity": "2", "unitPrice": "3.50"},
                {"warehouseSku": "WH-2", "quantity": "1", "amount": "9.99"},
            ]
        )

        self.assertEqual(len(lines), 2)
        self.assertEqual(str(lines[0]["amount"]), "7.00")
        self.assertEqual(str(lines[1]["amount"]), "9.99")

    def test_inventory_lines_reject_duplicate_warehouse_sku(self) -> None:
        with self.assertRaises(HTTPException) as error:
            normalize_inventory_lines(
                [
                    {"warehouseSku": "WH-1", "quantity": 1},
                    {"warehouseSku": "WH-1", "quantity": 2},
                ]
            )

        self.assertEqual(error.exception.status_code, 400)
        self.assertIn("duplicate warehouse SKU", error.exception.detail)

    def test_cost_modes_are_normalized_server_side(self) -> None:
        conn = FakeConnection({11: "quantity_x_unit", 12: "direct_amount"})
        costs = normalize_inventory_costs(
            conn,
            shop_key="default",
            cost_template_id=1,
            costs_payload=[
                {"costTypeId": 11, "quantity": "4", "unitPrice": "2.25", "amount": "100"},
                {"costTypeId": 12, "quantity": "9", "unitPrice": "9", "amount": "12.50"},
            ],
        )

        self.assertEqual(str(costs[0]["amount"]), "9.00")
        self.assertIsNone(costs[1]["quantity"])
        self.assertIsNone(costs[1]["unit_price"])
        self.assertEqual(str(costs[1]["amount"]), "12.50")

    def test_empty_cost_rows_are_not_saved(self) -> None:
        conn = FakeConnection({11: "quantity_x_unit"})
        costs = normalize_inventory_costs(
            conn,
            shop_key="default",
            cost_template_id=1,
            costs_payload=[{"costTypeId": 11, "quantity": "", "unitPrice": "", "amount": "", "note": ""}],
        )

        self.assertEqual(costs, [])



    def test_blank_template_rejects_cost_rows(self) -> None:
        conn = FakeConnection({11: "direct_amount"})
        with self.assertRaises(HTTPException) as error:
            normalize_inventory_costs(
                conn,
                shop_key="default",
                cost_template_id=None,
                costs_payload=[{"costTypeId": 11, "amount": "5"}],
            )

        self.assertEqual(error.exception.status_code, 400)
        self.assertIn("blank template", error.exception.detail)

    def test_cost_totals_are_grouped_by_dynamic_currency(self) -> None:
        totals = inventory_cost_totals(
            [
                {"currency": "RMB", "amount": 10},
                {"currency": "USD", "amount": 2.5},
                {"currency": "RMB", "amount": 3.25},
            ]
        )

        self.assertEqual(totals, {"RMB": 13.25, "USD": 2.5})

    def test_currency_is_normalized_and_validated(self) -> None:
        self.assertEqual(inventory_currency(" usd "), "USD")
        with self.assertRaises(HTTPException):
            inventory_currency("US-D")


if __name__ == "__main__":
    unittest.main()
