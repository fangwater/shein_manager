from __future__ import annotations

import pandas as pd

from shein_api_manager.pnl_web import daily_profit_trend


def test_daily_profit_trend_groups_by_day_and_calculates_sales_margin() -> None:
    frame = pd.DataFrame(
        {
            "order_created_dt": pd.to_datetime(
                ["2026-07-20 08:00:00", "2026-07-20 19:30:00", "2026-07-21 09:15:00"]
            ),
            "order_no": ["A", "B", "C"],
            "estimated_order_no": [pd.NA, "B", pd.NA],
            "gross_revenue_allocated_usd": [100.0, 50.0, 0.0],
            "profit_usd": [20.0, -5.0, -3.0],
            "sales_gross_revenue": [100.0, 0.0, 0.0],
            "sales_profit": [20.0, 0.0, 0.0],
        }
    )

    rows = daily_profit_trend(frame).to_dict(orient="records")

    assert len(rows) == 2
    assert rows[0]["day"] == pd.Timestamp("2026-07-20")
    assert rows[0]["orders"] == 2
    assert rows[0]["estimatedOrders"] == 1
    assert rows[0]["revenue"] == 150.0
    assert rows[0]["profit"] == 15.0
    assert rows[0]["margin"] == 0.2
    assert pd.isna(rows[1]["margin"])
