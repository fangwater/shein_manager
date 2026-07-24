from __future__ import annotations

from collections import defaultdict
from datetime import date, datetime
from decimal import Decimal
from typing import Any

import psycopg
from psycopg.rows import dict_row

from .xlwms_bridge import xlwms_database_url


def query_shein_cost_order_page(
    *,
    shein_database_url: str,
    shop_key: str,
    search: str = "",
    page: int = 1,
    page_size: int = 50,
) -> dict[str, Any]:
    search = search.strip()
    if page < 1:
        raise ValueError("page must be greater than or equal to 1")
    if not 1 <= page_size <= 200:
        raise ValueError("pageSize must be between 1 and 200")

    where = "WHERE shop_key = %s"
    params: list[Any] = [shop_key]
    if search:
        where += " AND order_no ILIKE %s"
        params.append(f"%{search}%")
    with psycopg.connect(shein_database_url, row_factory=dict_row) as conn:
        with conn.cursor() as cur:
            cur.execute(f"SELECT count(*) AS total FROM shein_orders {where}", params)
            total = int(cur.fetchone()["total"])
            cur.execute(
                f"""
                SELECT
                    order_no, order_status, order_status_label,
                    order_status_normalized, order_created_at, last_seen_at
                FROM shein_orders
                {where}
                ORDER BY nullif(order_created_at, '') DESC NULLS LAST, last_seen_at DESC
                LIMIT %s OFFSET %s
                """,
                [*params, page_size, (page - 1) * page_size],
            )
            shein_orders = cur.fetchall()

    order_numbers = [str(row["order_no"]) for row in shein_orders]
    relations_by_order: dict[str, list[dict[str, Any]]] = defaultdict(list)
    if order_numbers:
        with psycopg.connect(xlwms_database_url(), row_factory=dict_row) as conn:
            with conn.cursor() as cur:
                cur.execute(
                    """
                    WITH relations AS (
                        SELECT
                            wh_code,
                            platform_order_no,
                            order_no,
                            count(*) AS funds_flow_rows,
                            max(cost_time) AS last_cost_time,
                            max(last_seen_at) AS last_seen_at,
                            bool_or(detail_sync_status = 'success') AS detail_synced,
                            count(*) FILTER (
                                WHERE detail_sync_status = 'error'
                            ) AS error_rows
                        FROM xlwms_funds_flows
                        WHERE platform_order_no = ANY(%s)
                          AND coalesce(order_no, '') <> ''
                        GROUP BY wh_code, platform_order_no, order_no
                    )
                    SELECT
                        r.*,
                        count(d.cost_no) AS cost_detail_count
                    FROM relations r
                    LEFT JOIN xlwms_cost_details d
                      ON d.wh_code = r.wh_code
                     AND d.query_order_no = r.order_no
                     AND d.query_order_type = 1
                    GROUP BY
                        r.wh_code, r.platform_order_no, r.order_no,
                        r.funds_flow_rows, r.last_cost_time, r.last_seen_at,
                        r.detail_synced, r.error_rows
                    ORDER BY r.platform_order_no, r.last_cost_time DESC NULLS LAST
                    """,
                    (order_numbers,),
                )
                for row in cur.fetchall():
                    relation = _json_value(row)
                    relation["matched"] = True
                    relations_by_order[str(row["platform_order_no"])].append(relation)

    rows: list[dict[str, Any]] = []
    for shein_order in shein_orders:
        order_no = str(shein_order["order_no"])
        order_meta = {
            "shein_order_status": shein_order.get("order_status"),
            "shein_order_status_label": shein_order.get("order_status_label"),
            "shein_order_status_normalized": shein_order.get(
                "order_status_normalized"
            ),
            "shein_order_created_at": shein_order.get("order_created_at"),
        }
        relations = relations_by_order.get(order_no)
        if relations:
            rows.extend({**relation, **order_meta} for relation in relations)
        else:
            rows.append({
                "platform_order_no": order_no,
                "order_no": None,
                "wh_code": None,
                "funds_flow_rows": 0,
                "cost_detail_count": 0,
                "last_cost_time": None,
                "last_seen_at": None,
                "detail_synced": False,
                "error_rows": 0,
                "matched": False,
                **order_meta,
            })
    return {
        "rows": rows,
        "page": page,
        "pageSize": page_size,
        "total": total,
        "pages": (total + page_size - 1) // page_size,
    }


def _json_value(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: _json_value(item) for key, item in value.items()}
    if isinstance(value, Decimal):
        return float(value)
    if isinstance(value, (datetime, date)):
        return value.isoformat()
    return value
