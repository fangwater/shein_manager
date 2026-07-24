from __future__ import annotations

import json
import os
import subprocess
import threading
from copy import deepcopy
from datetime import date, datetime, timezone
from decimal import Decimal
from pathlib import Path
from typing import Any

import psycopg
from dotenv import dotenv_values
from psycopg.rows import dict_row


DEFAULT_XLWMS_ROOT = Path("/home/ubuntu/xlwms-api-manager")
DEFAULT_XLWMS_MANAGER = Path("/home/ubuntu/.venv/bin/xlwms-manager")
SYNC_DETAIL_LIMIT = 500
SYNC_TIMEOUT_SECONDS = 60 * 60

_SYNC_LOCK = threading.Lock()
_SYNC_STATE_LOCK = threading.Lock()
_SYNC_STATE: dict[str, Any] = {
    "running": False,
    "phase": "idle",
    "message": "尚未在本进程中执行同步",
    "startedAt": None,
    "finishedAt": None,
    "warehouses": [],
    "results": [],
    "error": None,
}


def xlwms_root() -> Path:
    return Path(os.getenv("XLWMS_PROJECT_ROOT", DEFAULT_XLWMS_ROOT)).expanduser()


def xlwms_database_url() -> str:
    configured = os.getenv("XLWMS_DATABASE_URL", "").strip()
    if configured:
        return configured
    env_file = Path(
        os.getenv("XLWMS_ENV_FILE", xlwms_root() / ".env")
    ).expanduser()
    value = str(dotenv_values(env_file).get("DATABASE_URL") or "").strip()
    if not value:
        raise ValueError(f"XLWMS DATABASE_URL is missing: {env_file}")
    return value


def query_cost_order_page(
    *,
    search: str = "",
    page: int = 1,
    page_size: int = 50,
) -> dict[str, Any]:
    search = search.strip()
    if page < 1:
        raise ValueError("page must be greater than or equal to 1")
    if not 1 <= page_size <= 200:
        raise ValueError("pageSize must be between 1 and 200")
    search_clause = ""
    params: list[Any] = []
    if search:
        search_clause = "WHERE platform_order_no ILIKE %s OR order_no ILIKE %s"
        pattern = f"%{search}%"
        params.extend((pattern, pattern))

    with psycopg.connect(xlwms_database_url(), row_factory=dict_row) as conn:
        with conn.cursor() as cur:
            cur.execute(
                f"""
                WITH relations AS (
                    SELECT
                        wh_code,
                        platform_order_no,
                        order_no,
                        count(*) AS funds_flow_rows,
                        max(cost_time) AS last_cost_time,
                        max(last_seen_at) AS last_seen_at,
                        bool_or(detail_sync_status = 'success') AS detail_synced,
                        count(*) FILTER (WHERE detail_sync_status = 'error') AS error_rows
                    FROM xlwms_funds_flows
                    WHERE coalesce(platform_order_no, '') <> ''
                      AND coalesce(order_no, '') <> ''
                    GROUP BY wh_code, platform_order_no, order_no
                ), filtered AS (
                    SELECT * FROM relations
                    {search_clause}
                )
                SELECT count(*) AS total FROM filtered
                """,
                params,
            )
            total = int(cur.fetchone()["total"])
            cur.execute(
                f"""
                WITH relations AS (
                    SELECT
                        wh_code,
                        platform_order_no,
                        order_no,
                        count(*) AS funds_flow_rows,
                        max(cost_time) AS last_cost_time,
                        max(last_seen_at) AS last_seen_at,
                        bool_or(detail_sync_status = 'success') AS detail_synced,
                        count(*) FILTER (WHERE detail_sync_status = 'error') AS error_rows
                    FROM xlwms_funds_flows
                    WHERE coalesce(platform_order_no, '') <> ''
                      AND coalesce(order_no, '') <> ''
                    GROUP BY wh_code, platform_order_no, order_no
                ), filtered AS (
                    SELECT * FROM relations
                    {search_clause}
                )
                SELECT
                    f.*,
                    count(d.cost_no) AS cost_detail_count
                FROM filtered f
                LEFT JOIN xlwms_cost_details d
                  ON d.wh_code = f.wh_code
                 AND d.query_order_no = f.order_no
                 AND d.query_order_type = 1
                GROUP BY
                    f.wh_code, f.platform_order_no, f.order_no,
                    f.funds_flow_rows, f.last_cost_time, f.last_seen_at,
                    f.detail_synced, f.error_rows
                ORDER BY f.last_cost_time DESC NULLS LAST, f.last_seen_at DESC
                LIMIT %s OFFSET %s
                """,
                [*params, page_size, (page - 1) * page_size],
            )
            rows = [_json_value(row) for row in cur.fetchall()]
    return {
        "rows": rows,
        "page": page,
        "pageSize": page_size,
        "total": total,
        "pages": (total + page_size - 1) // page_size,
    }


def query_cost_order_detail(platform_order_no: str) -> dict[str, Any]:
    platform_order_no = platform_order_no.strip()
    if not platform_order_no:
        raise ValueError("SHEIN order number is required")
    with psycopg.connect(xlwms_database_url(), row_factory=dict_row) as conn:
        with conn.cursor() as cur:
            cur.execute(
                """
                SELECT
                    wh_code, platform_order_no, order_no, cost_total,
                    currency_code, cost_status, module_type, cost_time,
                    bill_status, relate_bill_no, detail_sync_status,
                    detail_attempts, detail_last_attempt_at,
                    detail_error_code, detail_error_message
                FROM xlwms_funds_flows
                WHERE platform_order_no = %s
                ORDER BY cost_time DESC NULLS LAST, id DESC
                """,
                (platform_order_no,),
            )
            funds_flows = cur.fetchall()
            relations = sorted({
                (str(row["wh_code"]), str(row["order_no"]))
                for row in funds_flows
                if row.get("order_no")
            })
            cur.execute(
                """
                SELECT DISTINCT d.*
                FROM xlwms_cost_details d
                WHERE d.platform_order_no = %s
                   OR EXISTS (
                        SELECT 1
                        FROM xlwms_funds_flows f
                        WHERE f.platform_order_no = %s
                          AND f.wh_code = d.wh_code
                          AND f.order_no = d.query_order_no
                          AND d.query_order_type = 1
                   )
                ORDER BY d.create_time DESC NULLS LAST, d.cost_no
                """,
                (platform_order_no, platform_order_no),
            )
            details = cur.fetchall()
            items_by_cost: dict[tuple[str, str], list[dict[str, Any]]] = {}
            if details:
                keys = [(row["wh_code"], row["cost_no"]) for row in details]
                cur.execute(
                    """
                    SELECT
                        i.wh_code, i.cost_no, i.item_index, i.bill_item_name,
                        i.bill_item_total, i.charge_time
                    FROM xlwms_cost_items i
                    JOIN jsonb_to_recordset(%s::jsonb) AS wanted(wh_code text, cost_no text)
                      ON wanted.wh_code = i.wh_code AND wanted.cost_no = i.cost_no
                    ORDER BY i.wh_code, i.cost_no, i.item_index
                    """,
                    (json.dumps([
                        {"wh_code": wh_code, "cost_no": cost_no}
                        for wh_code, cost_no in keys
                    ]),),
                )
                for item in cur.fetchall():
                    key = (str(item["wh_code"]), str(item["cost_no"]))
                    items_by_cost.setdefault(key, []).append(_json_value(item))

    detail_rows = []
    for row in details:
        detail = _json_value(row)
        detail.pop("raw_payload", None)
        detail["items"] = items_by_cost.get(
            (str(row["wh_code"]), str(row["cost_no"])), []
        )
        detail_rows.append(detail)
    return {
        "platformOrderNo": platform_order_no,
        "relations": [
            {"wh_code": wh_code, "order_no": order_no}
            for wh_code, order_no in relations
        ],
        "fundsFlows": [_json_value(row) for row in funds_flows],
        "costDetails": detail_rows,
    }


def get_sync_status() -> dict[str, Any]:
    with _SYNC_STATE_LOCK:
        return deepcopy(_SYNC_STATE)


def start_xlwms_sync(*, detail_limit: int = SYNC_DETAIL_LIMIT) -> bool:
    if not 1 <= detail_limit <= 5000:
        raise ValueError("detailLimit must be between 1 and 5000")
    if not _SYNC_LOCK.acquire(blocking=False):
        return False
    now = _utc_text()
    _set_sync_state(
        running=True,
        phase="starting",
        message="正在准备 XLWMS 同步",
        startedAt=now,
        finishedAt=None,
        warehouses=[],
        results=[],
        error=None,
    )
    thread = threading.Thread(
        target=_run_xlwms_sync,
        kwargs={"detail_limit": detail_limit},
        daemon=True,
        name="xlwms-cost-sync",
    )
    thread.start()
    return True


def _run_xlwms_sync(*, detail_limit: int) -> None:
    try:
        database_url = xlwms_database_url()
        warehouses = _active_warehouse_codes(database_url)
        _set_sync_state(
            phase="funds_flow",
            message="正在同步资金流水",
            warehouses=warehouses,
        )
        results = [_run_manager(["sync-funds-flow", "--all-active"])]
        for index, warehouse in enumerate(warehouses, start=1):
            _set_sync_state(
                phase="cost_details",
                message=f"正在补费用明细 {index}/{len(warehouses)}：{warehouse}",
                results=results,
            )
            results.append(_run_manager([
                "sync-cost-details",
                "--warehouse",
                warehouse,
                "--workers",
                "4",
                "--requests-per-second",
                "8",
                "--limit",
                str(detail_limit),
            ]))
        _set_sync_state(
            running=False,
            phase="complete",
            message="XLWMS 资金流水和费用明细同步完成",
            finishedAt=_utc_text(),
            results=results,
            error=None,
        )
    except Exception as exc:  # The status API carries background failures to the UI.
        _set_sync_state(
            running=False,
            phase="error",
            message="XLWMS 同步失败",
            finishedAt=_utc_text(),
            error=str(exc)[:2000],
        )
    finally:
        _SYNC_LOCK.release()


def _active_warehouse_codes(database_url: str) -> list[str]:
    with psycopg.connect(database_url) as conn:
        with conn.cursor() as cur:
            cur.execute(
                "SELECT wh_code FROM xlwms_warehouses WHERE is_active ORDER BY wh_code"
            )
            return [str(row[0]) for row in cur.fetchall()]


def _run_manager(arguments: list[str]) -> dict[str, Any]:
    manager = Path(os.getenv("XLWMS_MANAGER_BIN", DEFAULT_XLWMS_MANAGER)).expanduser()
    if not manager.exists():
        raise ValueError(f"xlwms-manager executable is missing: {manager}")
    env = os.environ.copy()
    env_file = Path(
        os.getenv("XLWMS_ENV_FILE", xlwms_root() / ".env")
    ).expanduser()
    for key, value in dotenv_values(env_file).items():
        if value is not None:
            env[str(key)] = str(value)
    completed = subprocess.run(
        [str(manager), *arguments],
        cwd=xlwms_root(),
        env=env,
        capture_output=True,
        text=True,
        timeout=SYNC_TIMEOUT_SECONDS,
        check=False,
    )
    if completed.returncode != 0:
        error = completed.stderr.strip() or completed.stdout.strip() or "unknown error"
        raise RuntimeError(f"{' '.join(arguments)}: {error[-1800:]}")
    try:
        result = json.loads(completed.stdout)
    except json.JSONDecodeError:
        result = {"output": completed.stdout.strip()[-1000:]}
    return {"command": arguments[0], "result": result}


def _set_sync_state(**changes: Any) -> None:
    with _SYNC_STATE_LOCK:
        _SYNC_STATE.update(changes)


def _utc_text() -> str:
    return datetime.now(timezone.utc).isoformat()


def _json_value(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: _json_value(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [_json_value(item) for item in value]
    if isinstance(value, Decimal):
        return float(value)
    if isinstance(value, (datetime, date)):
        return value.isoformat()
    return value
