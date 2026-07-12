from __future__ import annotations

import argparse
import base64
import json
import sys
import urllib.parse
from pathlib import Path
from typing import Any

from .config import Settings, load_settings


def main(argv: list[str] | None = None) -> int:
    settings = load_settings()
    parser = build_parser(settings)
    args = parser.parse_args(argv)
    try:
        return args.func(args, settings)
    except Exception as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


def build_parser(settings: Settings) -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="shein-manager")
    sub = parser.add_subparsers(dest="command", required=True)

    init = sub.add_parser("init-db", help="Create PostgreSQL tables")
    init.set_defaults(func=cmd_init_db)

    auth_url = sub.add_parser("auth-url", help="Generate merchant authorization URL")
    auth_url.add_argument("--redirect-url", required=True)
    auth_url.add_argument("--state", default=settings.shop_key)
    auth_url.add_argument("--app-id", default=settings.app_id)
    auth_url.add_argument("--auth-base-url", default=settings.auth_base_url)
    auth_url.set_defaults(func=cmd_auth_url)

    exchange = sub.add_parser("exchange-token", help="Exchange tempToken for store credentials")
    exchange.add_argument("--temp-token", required=True)
    exchange.add_argument("--save-shop")
    exchange.add_argument("--state")
    exchange.add_argument("--api-base-url", default=settings.api_base_url)
    exchange.add_argument("--print-secret", action="store_true")
    exchange.set_defaults(func=cmd_exchange_token)

    save = sub.add_parser("save-shop", help="Save store credentials manually")
    save.add_argument("--shop-key", required=True)
    save.add_argument("--open-key-id", required=True)
    save.add_argument("--secret-key", required=True)
    save.add_argument("--api-base-url", default=settings.api_base_url)
    save.set_defaults(func=cmd_save_shop)

    order_list = sub.add_parser("order-list", help="Call /open-api/order/order-list")
    add_shop_args(order_list, settings)
    add_params_args(order_list)
    order_list.add_argument("--method", default=settings.order_list_method)
    order_list.set_defaults(func=cmd_order_list)

    order_detail = sub.add_parser("order-detail", help="Call /open-api/order/order-detail")
    add_shop_args(order_detail, settings)
    order_detail.add_argument("--order-no", action="append", required=True)
    order_detail.add_argument("--method", default=settings.order_detail_method)
    order_detail.add_argument("--body-field", default=settings.order_detail_field)
    order_detail.set_defaults(func=cmd_order_detail)

    product_list = sub.add_parser("product-list", help="Call /open-api/openapi-business-backend/product/query")
    add_shop_args(product_list, settings)
    add_params_args(product_list)
    product_list.add_argument("--method", default="POST")
    product_list.add_argument("--language", default="zh-cn")
    product_list.set_defaults(func=cmd_product_list)

    product_search = sub.add_parser("product-search", help="Call /open-api/goods/searchProduct")
    add_shop_args(product_search, settings)
    add_params_args(product_search)
    product_search.add_argument("--method", default="POST")
    product_search.add_argument("--language", default="zh-cn")
    product_search.set_defaults(func=cmd_product_search)

    sync_products = sub.add_parser("sync-products", help="Download SHEIN published product SKC/SKU codes")
    add_shop_args(sync_products, settings)
    add_params_args(sync_products)
    sync_products.add_argument("--method", default="POST")
    sync_products.add_argument("--language", default="zh-cn")
    sync_products.add_argument("--page-start", type=int, default=1)
    sync_products.add_argument("--page-size", type=int, default=500)
    sync_products.add_argument("--max-pages", type=int, default=100)
    sync_products.add_argument("--rps", type=float, default=20.0)
    sync_products.set_defaults(func=cmd_sync_products)

    sync_product_details = sub.add_parser("sync-product-details", help="Download SHEIN product details from /open-api/goods/searchProduct")
    add_shop_args(sync_product_details, settings)
    add_params_args(sync_product_details)
    sync_product_details.add_argument("--method", default="POST")
    sync_product_details.add_argument("--language", default="zh-cn")
    sync_product_details.add_argument("--page-start", type=int, default=1)
    sync_product_details.add_argument("--page-size", type=int, default=10)
    sync_product_details.add_argument("--max-pages", type=int, default=1000)
    sync_product_details.add_argument("--rps", type=float, default=20.0)
    sync_product_details.set_defaults(func=cmd_sync_product_details)


    return_list = sub.add_parser("return-list", help="Call /open-api/return-order/list")
    add_shop_args(return_list, settings)
    add_params_args(return_list)
    return_list.add_argument("--method", default=settings.return_order_list_method)
    return_list.set_defaults(func=cmd_return_list)

    return_detail = sub.add_parser("return-detail", help="Call /open-api/return-order/details")
    add_shop_args(return_detail, settings)
    return_detail.add_argument("--return-order-no", action="append", required=True)
    return_detail.add_argument("--method", default=settings.return_order_detail_method)
    return_detail.set_defaults(func=cmd_return_detail)

    sync_returns = sub.add_parser("sync-order-returns", help="Download SHEIN return-order list and details")
    add_shop_args(sync_returns, settings)
    add_params_args(sync_returns)
    sync_returns.add_argument("--list-method", default=settings.return_order_list_method)
    sync_returns.add_argument("--detail-method", default=settings.return_order_detail_method)
    sync_returns.add_argument("--page-start", type=int, default=1)
    sync_returns.add_argument("--page-size", type=int, default=30)
    sync_returns.add_argument("--max-pages", type=int, default=100)
    sync_returns.add_argument("--list-rps", type=float, default=1.0)
    sync_returns.add_argument("--detail-rps", type=float, default=1.0)
    sync_returns.add_argument("--no-fetch-details", action="store_true")
    sync_returns.set_defaults(func=cmd_sync_order_returns)

    download = sub.add_parser("download-orders", help="Download order list into PostgreSQL")
    add_shop_args(download, settings)
    add_params_args(download)
    download.add_argument("--list-method", default=settings.order_list_method)
    download.add_argument("--page-param", default="page")
    download.add_argument("--page-size-param", default="pageSize")
    download.add_argument("--page-start", type=int, default=1)
    download.add_argument("--page-size", type=int, default=30)
    download.add_argument("--max-pages", type=int, default=100)
    download.add_argument("--fetch-details", action="store_true")
    download.add_argument("--detail-method", default=settings.order_detail_method)
    download.add_argument("--detail-body-field", default=settings.order_detail_field)
    download.set_defaults(func=cmd_download_orders)

    sync_full = sub.add_parser("sync-orders-full", help="Resume-safe full order sync with details")
    add_shop_args(sync_full, settings)
    add_params_args(sync_full)
    sync_full.add_argument("--start-time", required=True, help="UTC+8, format YYYY-MM-DD HH:MM:SS")
    sync_full.add_argument("--end-time", required=True, help="UTC+8, format YYYY-MM-DD HH:MM:SS")
    sync_full.add_argument("--query-type", type=int, default=1, choices=(1, 2))
    sync_full.add_argument("--sync-key", help="Stable key used to resume a sync job")
    sync_full.add_argument("--reset", action="store_true", help="Clear progress for this sync key and run from the beginning")
    sync_full.add_argument("--list-method", default=settings.order_list_method)
    sync_full.add_argument("--detail-method", default=settings.order_detail_method)
    sync_full.add_argument("--detail-body-field", default=settings.order_detail_field)
    sync_full.add_argument("--page-size", type=int, default=30)
    sync_full.add_argument("--window-hours", type=float, default=48.0)
    sync_full.add_argument("--list-rps", type=float, default=20.0)
    sync_full.add_argument("--detail-rps", type=float, default=50.0)
    sync_full.set_defaults(func=cmd_sync_orders_full)

    backfill_returns = sub.add_parser(
        "backfill-order-returns",
        help="Backfill official order status/type labels and clear legacy inferred return flags",
    )
    backfill_returns.add_argument("--shop-key", default=settings.shop_key)
    backfill_returns.add_argument("--all-shops", action="store_true")
    backfill_returns.set_defaults(func=cmd_backfill_order_returns)

    return parser


def add_shop_args(parser: argparse.ArgumentParser, settings: Settings) -> None:
    parser.add_argument("--shop-key", default=settings.shop_key)
    parser.add_argument("--api-base-url", default=settings.api_base_url)


def add_params_args(parser: argparse.ArgumentParser) -> None:
    source = parser.add_mutually_exclusive_group()
    source.add_argument("--params-json", default="{}")
    source.add_argument("--params-file")
    parser.add_argument("--set", action="append", default=[], help="Extra request parameter as key=value")


def cmd_init_db(args: argparse.Namespace, settings: Settings) -> int:
    from .db import init_db

    database_url = require_database_url(settings)
    init_db(database_url)
    print("PostgreSQL tables are ready.")
    return 0


def cmd_auth_url(args: argparse.Namespace, settings: Settings) -> int:
    if not args.app_id:
        raise ValueError("SHEIN_APP_ID is required")
    redirect_encoded = base64.b64encode(args.redirect_url.encode("utf-8")).decode("utf-8")
    query = urllib.parse.urlencode(
        {
            "appid": args.app_id,
            "redirectUrl": redirect_encoded,
            "state": args.state,
        }
    )
    print(f"{args.auth_base_url.rstrip('/')}/#/empower?{query}")
    return 0


def cmd_exchange_token(args: argparse.Namespace, settings: Settings) -> int:
    from .client import SheinClient

    if not settings.app_id or not settings.app_secret_key:
        raise ValueError("SHEIN_APP_ID and SHEIN_APP_SECRET_KEY are required")
    client = SheinClient(
        base_url=args.api_base_url,
        app_id=settings.app_id,
        app_secret_key=settings.app_secret_key,
    )
    response = client.exchange_temp_token(args.temp_token)
    info = response.get("info") or {}
    open_key_id = info.get("openKeyId")
    secret_key = info.get("secretKey")
    if not open_key_id or not secret_key:
        raise ValueError(f"get-by-token did not return openKeyId/secretKey: {response}")

    if args.save_shop:
        from .db import save_shop

        database_url = require_database_url(settings)
        save_shop(
            database_url,
            shop_key=args.save_shop,
            app_id=settings.app_id,
            open_key_id=open_key_id,
            secret_key=secret_key,
            base_url=args.api_base_url,
            state=args.state or info.get("state"),
        )

    output = {
        "openKeyId": open_key_id,
        "secretKey": secret_key if args.print_secret else "***",
        "savedShop": args.save_shop,
    }
    print(json.dumps(output, ensure_ascii=False, indent=2))
    return 0


def cmd_save_shop(args: argparse.Namespace, settings: Settings) -> int:
    from .db import save_shop

    database_url = require_database_url(settings)
    save_shop(
        database_url,
        shop_key=args.shop_key,
        open_key_id=args.open_key_id,
        secret_key=args.secret_key,
        base_url=args.api_base_url,
        app_id=settings.app_id,
    )
    print(f"Saved shop credentials for {args.shop_key}.")
    return 0


def cmd_order_list(args: argparse.Namespace, settings: Settings) -> int:
    from .orders import OrderService, client_from_credentials

    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    service = OrderService(client_from_credentials(credentials))
    response = service.order_list(load_params(args), method=args.method)
    print(json.dumps(response, ensure_ascii=False, indent=2))
    return 0


def cmd_order_detail(args: argparse.Namespace, settings: Settings) -> int:
    from .orders import OrderService, client_from_credentials

    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    service = OrderService(client_from_credentials(credentials))
    response = service.order_detail(args.order_no, method=args.method, body_field=args.body_field)
    print(json.dumps(response, ensure_ascii=False, indent=2))
    return 0





def cmd_product_list(args: argparse.Namespace, settings: Settings) -> int:
    from .products import ProductService, client_from_credentials

    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    service = ProductService(client_from_credentials(credentials))
    response = service.product_list(load_params(args), method=args.method, language=args.language)
    print(json.dumps(response, ensure_ascii=False, indent=2))
    return 0


def cmd_product_search(args: argparse.Namespace, settings: Settings) -> int:
    from .products import ProductService, client_from_credentials

    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    service = ProductService(client_from_credentials(credentials))
    response = service.search_products(load_params(args), method=args.method, language=args.language)
    print(json.dumps(response, ensure_ascii=False, indent=2))
    return 0


def cmd_sync_products(args: argparse.Namespace, settings: Settings) -> int:
    from .products import sync_products

    database_url = require_database_url(settings)
    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    result = sync_products(
        database_url=database_url,
        credentials=credentials,
        query_params=load_params(args),
        method=args.method,
        page_start=args.page_start,
        page_size=args.page_size,
        max_pages=args.max_pages,
        rps=args.rps,
        language=args.language,
    )
    print(
        json.dumps(
            {
                "pages": result.pages,
                "productsSeen": result.products_seen,
                "productsInserted": result.products_upserted,
                "skuCodesSeen": result.sku_codes_seen,
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


def cmd_sync_product_details(args: argparse.Namespace, settings: Settings) -> int:
    from .products import sync_product_details

    database_url = require_database_url(settings)
    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    result = sync_product_details(
        database_url=database_url,
        credentials=credentials,
        query_params=load_params(args),
        method=args.method,
        page_start=args.page_start,
        page_size=args.page_size,
        max_pages=args.max_pages,
        rps=args.rps,
        language=args.language,
    )
    print(
        json.dumps(
            {
                "pages": result.pages,
                "productsSeen": result.products_seen,
                "productsInserted": result.products_upserted,
                "skuCodesSeen": result.sku_codes_seen,
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


def cmd_return_list(args: argparse.Namespace, settings: Settings) -> int:
    from .returns_api import ReturnService, client_from_credentials

    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    service = ReturnService(client_from_credentials(credentials))
    response = service.order_list(load_params(args), method=args.method)
    print(json.dumps(response, ensure_ascii=False, indent=2))
    return 0


def cmd_return_detail(args: argparse.Namespace, settings: Settings) -> int:
    from .returns_api import ReturnService, client_from_credentials

    if len(args.return_order_no) > 30:
        raise ValueError("return-order/details accepts at most 30 returnOrderNo values")
    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    service = ReturnService(client_from_credentials(credentials))
    response = service.order_details(args.return_order_no, method=args.method)
    print(json.dumps(response, ensure_ascii=False, indent=2))
    return 0


def cmd_sync_order_returns(args: argparse.Namespace, settings: Settings) -> int:
    from .returns_api import sync_order_returns

    database_url = require_database_url(settings)
    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    result = sync_order_returns(
        database_url=database_url,
        credentials=credentials,
        query_params=load_params(args),
        order_list_path=settings.return_order_list_path,
        order_detail_path=settings.return_order_detail_path,
        list_method=args.list_method,
        detail_method=args.detail_method,
        page_start=args.page_start,
        page_size=args.page_size,
        max_pages=args.max_pages,
        list_rps=args.list_rps,
        detail_rps=args.detail_rps,
        fetch_details=not args.no_fetch_details,
    )
    print(
        json.dumps(
            {
                "listPages": result.list_pages,
                "listReturnsSeen": result.list_returns_seen,
                "listReturnsInserted": result.list_returns_upserted,
                "returnOrderNos": result.return_order_nos,
                "detailBatches": result.detail_batches,
                "detailsSeen": result.details_seen,
                "detailsInserted": result.details_upserted,
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0

def cmd_download_orders(args: argparse.Namespace, settings: Settings) -> int:
    from .orders import download_orders

    database_url = require_database_url(settings)
    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    result = download_orders(
        database_url=database_url,
        credentials=credentials,
        query_params=load_params(args),
        list_method=args.list_method,
        page_param=args.page_param,
        page_size_param=args.page_size_param,
        page_start=args.page_start,
        page_size=args.page_size,
        max_pages=args.max_pages,
        fetch_details=args.fetch_details,
        detail_method=args.detail_method,
        detail_body_field=args.detail_body_field,
    )
    print(
        json.dumps(
            {
                "runId": result.run_id,
                "pages": result.pages,
                "ordersSeen": result.orders_seen,
                "ordersInserted": result.orders_upserted,
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


def cmd_sync_orders_full(args: argparse.Namespace, settings: Settings) -> int:
    from .orders import sync_orders_full

    database_url = require_database_url(settings)
    credentials = resolve_credentials(args.shop_key, args.api_base_url, settings)
    result = sync_orders_full(
        database_url=database_url,
        credentials=credentials,
        start_time=args.start_time,
        end_time=args.end_time,
        query_type=args.query_type,
        extra_params=load_params(args),
        sync_key=args.sync_key,
        list_method=args.list_method,
        detail_method=args.detail_method,
        detail_body_field=args.detail_body_field,
        page_size=args.page_size,
        window_hours=args.window_hours,
        list_rps=args.list_rps,
        detail_rps=args.detail_rps,
        reset=args.reset,
    )
    print(
        json.dumps(
            {
                "jobId": result.job_id,
                "syncKey": result.sync_key,
                "status": result.status,
                "windowsTotal": result.windows_total,
                "windowsCompleted": result.windows_completed,
                "pages": result.pages,
                "ordersSeen": result.orders_seen,
                "ordersInserted": result.orders_upserted,
                "detailsFetched": result.details_fetched,
            },
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0


def cmd_backfill_order_returns(args: argparse.Namespace, settings: Settings) -> int:
    from .db import backfill_order_returns, init_db

    database_url = require_database_url(settings)
    init_db(database_url)
    result = backfill_order_returns(
        database_url,
        shop_key=None if args.all_shops else args.shop_key,
    )
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


def load_params(args: argparse.Namespace) -> dict[str, Any]:
    if args.params_file:
        raw = Path(args.params_file).read_text(encoding="utf-8")
    else:
        raw = args.params_json
    params = json.loads(raw or "{}")
    if not isinstance(params, dict):
        raise ValueError("params must be a JSON object")
    for item in args.set:
        if "=" not in item:
            raise ValueError(f"--set must be key=value: {item}")
        key, value = item.split("=", 1)
        params[key] = parse_scalar(value)
    return params


def parse_scalar(value: str) -> Any:
    lowered = value.lower()
    if lowered == "true":
        return True
    if lowered == "false":
        return False
    if lowered == "null":
        return None
    try:
        return int(value)
    except ValueError:
        pass
    return value


def resolve_credentials(shop_key: str, api_base_url: str, settings: Settings) -> StoreCredentials:
    if settings.open_key_id and settings.secret_key:
        from .client import StoreCredentials

        return StoreCredentials(
            shop_key=shop_key,
            open_key_id=settings.open_key_id,
            secret_key=settings.secret_key,
            base_url=api_base_url,
        )
    database_url = require_database_url(settings)
    from .db import load_shop

    return load_shop(database_url, shop_key)


def require_database_url(settings: Settings) -> str:
    if not settings.database_url:
        raise ValueError("DATABASE_URL is required")
    return settings.database_url


if __name__ == "__main__":
    raise SystemExit(main())
