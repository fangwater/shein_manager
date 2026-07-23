from __future__ import annotations

import pandas as pd
from fastapi.testclient import TestClient

from shein_api_manager.pnl_web import (
    PNL_ORDER_MAX_PAGE_SIZE,
    TEMPLATE_DIR,
    app,
    paginate_pnl_orders,
)


def order_rows() -> pd.DataFrame:
    return pd.DataFrame([
        {
            "order_no": "ORD-1",
            "skus": "WH-A",
            "sheinSkus": "S-A",
            "skcs": "SKC-A",
            "returnStatus": "",
            "profit": 1.0,
        },
        {
            "order_no": "ORD-2",
            "skus": "WH-B",
            "sheinSkus": "S-B",
            "skcs": "SKC-B",
            "returnStatus": "returned",
            "profit": -5.0,
        },
        {
            "order_no": "ORD-3",
            "skus": "WH-C",
            "sheinSkus": "S-C",
            "skcs": "SKC-C",
            "returnStatus": "",
            "profit": 8.0,
        },
    ])


def test_paginate_pnl_orders_sorts_and_clamps_page() -> None:
    rows, pagination = paginate_pnl_orders(
        order_rows(),
        page=99,
        page_size=2,
        sort_key="profit",
        direction="asc",
    )

    assert rows["order_no"].tolist() == ["ORD-3"]
    assert pagination == {"page": 2, "pageSize": 2, "pages": 2, "total": 3}


def test_paginate_pnl_orders_searches_across_order_dimensions() -> None:
    rows, pagination = paginate_pnl_orders(
        order_rows(),
        search="wh-b",
        page_size=PNL_ORDER_MAX_PAGE_SIZE + 50,
    )

    assert rows["order_no"].tolist() == ["ORD-2"]
    assert pagination["pageSize"] == PNL_ORDER_MAX_PAGE_SIZE
    assert pagination["total"] == 1


def test_dashboard_uses_same_origin_echarts_asset() -> None:
    dashboard = (TEMPLATE_DIR / "dashboard.html").read_text(encoding="utf-8")

    assert 'src="assets/echarts-5.5.1.min.js"' in dashboard
    assert "cdn.jsdelivr.net" not in dashboard


def test_echarts_asset_is_served_with_long_lived_cache() -> None:
    with TestClient(app) as client:
        response = client.get("/assets/echarts-5.5.1.min.js")

    assert response.status_code == 200
    assert response.headers["content-type"].startswith("text/javascript")
    assert "immutable" in response.headers["cache-control"]
    assert len(response.content) > 1_000_000
    assert b"Licensed to the Apache Software Foundation" in response.content[:500]
