#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any
from zoneinfo import ZoneInfo


REPO_ROOT = Path(__file__).resolve().parents[1]
SRC_DIR = REPO_ROOT / "src"
if str(SRC_DIR) not in sys.path:
    sys.path.insert(0, str(SRC_DIR))

from shein_api_manager import db
from shein_api_manager.client import StoreCredentials
from shein_api_manager.config import Settings, load_settings
from shein_api_manager.orders import (
    MAX_ORDER_LIST_PAGE_SIZE,
    build_time_windows,
    format_shein_time,
    parse_shein_time,
    sync_orders_full,
)
from shein_api_manager.products import (
    MAX_PRODUCT_SEARCH_PAGE_SIZE,
    sync_product_details,
    sync_products,
)
from shein_api_manager.returns_api import MAX_LIST_PAGE_SIZE, sync_order_returns


LOCAL_TZ = ZoneInfo("Asia/Shanghai")


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    settings = load_settings()
    if not settings.database_url:
        raise ValueError("DATABASE_URL is required")

    shop_key = args.shop_key or settings.shop_key
    api_base_url = (args.api_base_url or settings.api_base_url).rstrip("/")
    credentials = resolve_credentials(settings, shop_key=shop_key, api_base_url=api_base_url)
    db.init_db(settings.database_url, shop_key=shop_key)
    database_url = db.shop_database_url(settings.database_url, shop_key)

    selected = set(args.data or ["orders", "returns", "products"])
    end_dt = parse_optional_shein_time(args.end_time) or local_now()
    result: dict[str, Any] = {
        "shopKey": shop_key,
        "endTime": format_shein_time(end_dt),
        "dryRun": args.dry_run,
    }

    if "orders" in selected:
        result["orders"] = run_orders(database_url, credentials, args, end_dt)
    if "returns" in selected:
        result["returns"] = run_returns(database_url, credentials, settings, args, end_dt)
    if "products" in selected:
        result["products"] = run_products(database_url, credentials, args, end_dt)

    print(json.dumps(result, ensure_ascii=False, indent=2, default=str))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Sync latest SHEIN orders, returns, and products from PostgreSQL progress.",
    )
    parser.add_argument(
        "--data",
        action="append",
        choices=("orders", "returns", "products"),
        help="Data type to sync. Repeat to sync multiple types. Defaults to all.",
    )
    parser.add_argument("--shop-key", help="Defaults to SHEIN_SHOP_KEY or beauty-hangers-home.")
    parser.add_argument("--api-base-url", help="Defaults to SHEIN_API_BASE_URL.")
    parser.add_argument("--end-time", help="UTC+8, format YYYY-MM-DD HH:MM:SS. Defaults to now.")
    parser.add_argument(
        "--fallback-days",
        type=int,
        default=30,
        help="When the database has no progress, start this many days before end-time.",
    )
    parser.add_argument(
        "--overlap-minutes",
        type=int,
        default=60,
        help="Move database progress back by this many minutes to avoid boundary misses.",
    )
    parser.add_argument("--dry-run", action="store_true", help="Print planned ranges without calling SHEIN.")

    parser.add_argument("--orders-start-time", help="Override order sync start time.")
    parser.add_argument(
        "--orders-query-type",
        type=int,
        default=2,
        choices=(1, 2),
        help="1 = order allocation/create time, 2 = update time. Default is 2.",
    )
    parser.add_argument("--orders-sync-key", help="Optional resume key for sync-orders-full.")
    parser.add_argument("--orders-page-size", type=int, default=MAX_ORDER_LIST_PAGE_SIZE)
    parser.add_argument("--orders-window-hours", type=float, default=48.0)
    parser.add_argument("--orders-list-rps", type=float, default=20.0)
    parser.add_argument("--orders-detail-rps", type=float, default=50.0)

    parser.add_argument("--returns-start-time", help="Override return sync start time.")
    parser.add_argument(
        "--returns-query-type",
        type=int,
        default=2,
        choices=(1, 2),
        help="1 = return request time, 2 = update time. Default is 2.",
    )
    parser.add_argument("--returns-page-size", type=int, default=MAX_LIST_PAGE_SIZE)
    parser.add_argument("--returns-window-hours", type=float, default=48.0)
    parser.add_argument("--returns-list-rps", type=float, default=1.0)
    parser.add_argument("--returns-detail-rps", type=float, default=1.0)
    parser.add_argument("--returns-no-details", action="store_true")

    parser.add_argument("--products-start-time", help="Override product sync start time.")
    parser.add_argument(
        "--products-mode",
        choices=("details", "basic", "both"),
        default="details",
        help="details uses /open-api/goods/searchProduct; basic uses product/query. Default is details.",
    )
    parser.add_argument("--products-page-size", type=int, default=MAX_PRODUCT_SEARCH_PAGE_SIZE)
    parser.add_argument("--products-basic-page-size", type=int, default=500)
    parser.add_argument("--products-max-pages", type=int, default=1000)
    parser.add_argument("--products-rps", type=float, default=20.0)
    parser.add_argument("--products-language", default="zh-cn")
    return parser


def run_orders(
    database_url: str,
    credentials: StoreCredentials,
    args: argparse.Namespace,
    end_dt: datetime,
) -> dict[str, Any]:
    start_dt = resolve_start_time(
        override=args.orders_start_time,
        progress=get_order_progress(database_url, credentials.shop_key, query_type=args.orders_query_type),
        end_dt=end_dt,
        fallback_days=args.fallback_days,
        overlap_minutes=args.overlap_minutes,
    )
    plan = {
        "startTime": format_shein_time(start_dt),
        "endTime": format_shein_time(end_dt),
        "queryType": args.orders_query_type,
    }
    if start_dt > end_dt:
        return {**plan, "skipped": True, "reason": "startTime is after endTime"}
    if args.dry_run:
        return {**plan, "skipped": False}

    sync_key = args.orders_sync_key or (
        f"latest-orders-q{args.orders_query_type}-"
        f"{start_dt:%Y%m%d%H%M%S}-{end_dt:%Y%m%d%H%M%S}"
    )
    result = sync_orders_full(
        database_url=database_url,
        credentials=credentials,
        start_time=format_shein_time(start_dt),
        end_time=format_shein_time(end_dt),
        query_type=args.orders_query_type,
        extra_params={},
        sync_key=sync_key,
        list_method="POST",
        detail_method="POST",
        detail_body_field="orderNoList",
        page_size=args.orders_page_size,
        window_hours=args.orders_window_hours,
        list_rps=args.orders_list_rps,
        detail_rps=args.orders_detail_rps,
    )
    return {
        **plan,
        "syncKey": result.sync_key,
        "status": result.status,
        "windowsTotal": result.windows_total,
        "windowsCompleted": result.windows_completed,
        "pages": result.pages,
        "ordersSeen": result.orders_seen,
        "ordersInserted": result.orders_upserted,
        "detailsFetched": result.details_fetched,
    }


def run_returns(
    database_url: str,
    credentials: StoreCredentials,
    settings: Settings,
    args: argparse.Namespace,
    end_dt: datetime,
) -> dict[str, Any]:
    start_dt = resolve_start_time(
        override=args.returns_start_time,
        progress=get_return_progress(database_url, credentials.shop_key, query_type=args.returns_query_type),
        end_dt=end_dt,
        fallback_days=args.fallback_days,
        overlap_minutes=args.overlap_minutes,
    )
    windows = build_time_windows(start_dt, end_dt, window_hours=args.returns_window_hours) if start_dt <= end_dt else []
    plan = {
        "startTime": format_shein_time(start_dt),
        "endTime": format_shein_time(end_dt),
        "queryType": args.returns_query_type,
        "windows": len(windows),
    }
    if start_dt > end_dt:
        return {**plan, "skipped": True, "reason": "startTime is after endTime"}
    if args.dry_run:
        return {**plan, "skipped": False}

    totals = {
        "listPages": 0,
        "listReturnsSeen": 0,
        "listReturnsInserted": 0,
        "returnOrderNos": 0,
        "detailBatches": 0,
        "detailsSeen": 0,
        "detailsInserted": 0,
    }
    for _, window_start, window_end in windows:
        result = sync_order_returns(
            database_url=database_url,
            credentials=credentials,
            query_params={
                "startTime": window_start,
                "endTime": window_end,
                "queryType": args.returns_query_type,
            },
            order_list_path=settings.return_order_list_path,
            order_detail_path=settings.return_order_detail_path,
            list_method=settings.return_order_list_method,
            detail_method=settings.return_order_detail_method,
            page_size=args.returns_page_size,
            max_pages=1000,
            list_rps=args.returns_list_rps,
            detail_rps=args.returns_detail_rps,
            fetch_details=not args.returns_no_details,
        )
        totals["listPages"] += result.list_pages
        totals["listReturnsSeen"] += result.list_returns_seen
        totals["listReturnsInserted"] += result.list_returns_upserted
        totals["returnOrderNos"] += result.return_order_nos
        totals["detailBatches"] += result.detail_batches
        totals["detailsSeen"] += result.details_seen
        totals["detailsInserted"] += result.details_upserted

    return {**plan, **totals}


def run_products(
    database_url: str,
    credentials: StoreCredentials,
    args: argparse.Namespace,
    end_dt: datetime,
) -> dict[str, Any]:
    start_dt = resolve_start_time(
        override=args.products_start_time,
        progress=get_product_progress(database_url, credentials.shop_key, mode=args.products_mode),
        end_dt=end_dt,
        fallback_days=args.fallback_days,
        overlap_minutes=args.overlap_minutes,
    )
    plan = {
        "startTime": format_shein_time(start_dt),
        "endTime": format_shein_time(end_dt),
        "mode": args.products_mode,
        "progressNote": "Product APIs are queried by create/insert time; existing tables only store last_seen_at as the default cursor.",
    }
    if start_dt > end_dt:
        return {**plan, "skipped": True, "reason": "startTime is after endTime"}
    if args.dry_run:
        return {**plan, "skipped": False}

    totals: dict[str, Any] = {}
    if args.products_mode in {"details", "both"}:
        result = sync_product_details(
            database_url=database_url,
            credentials=credentials,
            query_params={
                "createTimeStart": format_shein_time(start_dt),
                "createTimeEnd": format_shein_time(end_dt),
                "languageList": [args.products_language],
            },
            page_size=args.products_page_size,
            max_pages=args.products_max_pages,
            rps=args.products_rps,
            language=args.products_language,
        )
        totals["details"] = {
            "pages": result.pages,
            "productsSeen": result.products_seen,
            "productsInserted": result.products_upserted,
            "skuCodesSeen": result.sku_codes_seen,
        }
    if args.products_mode in {"basic", "both"}:
        result = sync_products(
            database_url=database_url,
            credentials=credentials,
            query_params={
                "insertTimeStart": format_shein_time(start_dt),
                "insertTimeEnd": format_shein_time(end_dt),
            },
            page_size=args.products_basic_page_size,
            max_pages=args.products_max_pages,
            rps=args.products_rps,
            language=args.products_language,
        )
        totals["basic"] = {
            "pages": result.pages,
            "productsSeen": result.products_seen,
            "productsInserted": result.products_upserted,
            "skuCodesSeen": result.sku_codes_seen,
        }
    return {**plan, **totals}


def resolve_credentials(settings: Settings, *, shop_key: str, api_base_url: str) -> StoreCredentials:
    if settings.open_key_id and settings.secret_key:
        return StoreCredentials(
            shop_key=shop_key,
            open_key_id=settings.open_key_id,
            secret_key=settings.secret_key,
            base_url=api_base_url,
        )
    if not settings.database_url:
        raise ValueError("DATABASE_URL is required")
    return db.load_shop(settings.database_url, shop_key)


def resolve_start_time(
    *,
    override: str | None,
    progress: datetime | None,
    end_dt: datetime,
    fallback_days: int,
    overlap_minutes: int,
) -> datetime:
    if override:
        return parse_shein_time(override)
    if progress is None:
        return end_dt - timedelta(days=fallback_days)
    return progress - timedelta(minutes=overlap_minutes)


def get_order_progress(database_url: str, shop_key: str, *, query_type: int) -> datetime | None:
    with db.connect(database_url) as conn:
        if query_type == 2:
            row = conn.execute(
                """
                SELECT max(order_updated_at), max(order_created_at), max(last_seen_at)
                FROM shein_orders
                WHERE shop_key = %s
                """,
                (shop_key,),
            ).fetchone()
        else:
            row = conn.execute(
                """
                SELECT max(order_created_at), max(order_updated_at), max(last_seen_at)
                FROM shein_orders
                WHERE shop_key = %s
                """,
                (shop_key,),
            ).fetchone()
    return parse_progress_datetime(row[0]) or parse_progress_datetime(row[1]) or parse_progress_datetime(row[2])


def get_return_progress(database_url: str, shop_key: str, *, query_type: int) -> datetime | None:
    with db.connect(database_url) as conn:
        if query_type == 2:
            row = conn.execute(
                """
                SELECT max(last_update_time), max(request_return_time), max(last_seen_at)
                FROM shein_order_returns
                WHERE shop_key = %s
                """,
                (shop_key,),
            ).fetchone()
        else:
            row = conn.execute(
                """
                SELECT max(request_return_time), max(last_update_time), max(last_seen_at)
                FROM shein_order_returns
                WHERE shop_key = %s
                """,
                (shop_key,),
            ).fetchone()
    return parse_progress_datetime(row[0]) or parse_progress_datetime(row[1]) or parse_progress_datetime(row[2])


def get_product_progress(database_url: str, shop_key: str, *, mode: str) -> datetime | None:
    with db.connect(database_url) as conn:
        if mode == "basic":
            row = conn.execute(
                "SELECT max(last_seen_at) FROM shein_products WHERE shop_key = %s",
                (shop_key,),
            ).fetchone()
        elif mode == "details":
            row = conn.execute(
                "SELECT max(last_seen_at), max(detail_fetched_at) FROM shein_product_details WHERE shop_key = %s",
                (shop_key,),
            ).fetchone()
        else:
            row = conn.execute(
                """
                SELECT
                    (SELECT max(last_seen_at) FROM shein_product_details WHERE shop_key = %s),
                    (SELECT max(last_seen_at) FROM shein_products WHERE shop_key = %s)
                """,
                (shop_key, shop_key),
            ).fetchone()
    return latest_datetime(*row)


def latest_datetime(*values: Any) -> datetime | None:
    parsed = [value for value in (parse_progress_datetime(value) for value in values) if value is not None]
    if not parsed:
        return None
    return max(parsed)


def parse_progress_datetime(value: Any) -> datetime | None:
    if value in (None, ""):
        return None
    if isinstance(value, datetime):
        if value.tzinfo is not None:
            return value.astimezone(LOCAL_TZ).replace(tzinfo=None)
        return value
    text = str(value).strip()
    if not text:
        return None
    normalized = text.replace("T", " ")
    if "." in normalized:
        normalized = normalized.split(".", 1)[0]
    for suffix in ("+0800", "+08:00", "Z"):
        if normalized.endswith(suffix):
            normalized = normalized[: -len(suffix)]
    normalized = normalized.strip()
    for fmt in ("%Y-%m-%d %H:%M:%S", "%Y-%m-%d %H:%M"):
        try:
            return datetime.strptime(normalized, fmt)
        except ValueError:
            pass
    return None


def parse_optional_shein_time(value: str | None) -> datetime | None:
    return parse_shein_time(value) if value else None


def local_now() -> datetime:
    return datetime.now(LOCAL_TZ).replace(tzinfo=None, microsecond=0)


if __name__ == "__main__":
    raise SystemExit(main())
