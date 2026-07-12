from __future__ import annotations

from dataclasses import dataclass
import time
from typing import Any

from . import db
from .client import SheinClient, StoreCredentials


PRODUCT_QUERY_PATH = "/open-api/openapi-business-backend/product/query"
PRODUCT_SEARCH_PATH = "/open-api/goods/searchProduct"
MAX_PRODUCT_QUERY_RPS = 100.0
MAX_PRODUCT_QUERY_PAGE_SIZE = 50000
DEFAULT_PRODUCT_QUERY_PAGE_SIZE = 500
MAX_PRODUCT_SEARCH_PAGE_SIZE = 10
DEFAULT_LANGUAGE = "zh-cn"


@dataclass(frozen=True)
class ProductSyncResult:
    pages: int
    products_seen: int
    products_upserted: int
    sku_codes_seen: int


class ProductService:
    def __init__(
        self,
        client: SheinClient,
        *,
        product_query_path: str = PRODUCT_QUERY_PATH,
        product_search_path: str = PRODUCT_SEARCH_PATH,
    ) -> None:
        self.client = client
        self.product_query_path = product_query_path
        self.product_search_path = product_search_path

    def product_list(
        self,
        params: dict[str, Any],
        *,
        method: str = "POST",
        language: str = DEFAULT_LANGUAGE,
    ) -> dict[str, Any]:
        method = method.upper()
        if method == "GET":
            return self.client.request("GET", self.product_query_path, params=params, language=language)
        return self.client.request(method, self.product_query_path, json_body=params, language=language)

    def search_products(
        self,
        params: dict[str, Any],
        *,
        method: str = "POST",
        language: str = DEFAULT_LANGUAGE,
    ) -> dict[str, Any]:
        method = method.upper()
        if method == "GET":
            return self.client.request("GET", self.product_search_path, params=params, language=language)
        return self.client.request(method, self.product_search_path, json_body=params, language=language)


def client_from_credentials(credentials: StoreCredentials) -> SheinClient:
    return SheinClient(
        base_url=credentials.base_url,
        open_key_id=credentials.open_key_id,
        secret_key=credentials.secret_key,
    )


def sync_products(
    *,
    database_url: str,
    credentials: StoreCredentials,
    query_params: dict[str, Any],
    method: str = "POST",
    page_start: int = 1,
    page_size: int = DEFAULT_PRODUCT_QUERY_PAGE_SIZE,
    max_pages: int = 100,
    rps: float = 20.0,
    language: str = DEFAULT_LANGUAGE,
) -> ProductSyncResult:
    validate_page_options(
        query_params=query_params,
        page_start=page_start,
        page_size=page_size,
        max_pages=max_pages,
        rps=rps,
        max_page_size=MAX_PRODUCT_QUERY_PAGE_SIZE,
        reserved_name="product sync",
    )
    db.init_db(database_url)
    service = ProductService(client_from_credentials(credentials))
    limiter = RateLimiter(rps)
    pages = 0
    products_seen = 0
    products_upserted = 0
    sku_codes_seen: set[str] = set()

    for page_offset in range(max_pages):
        page_no = page_start + page_offset
        params = dict(query_params)
        params["pageNum"] = page_no
        params["pageSize"] = page_size

        limiter.wait()
        response = service.product_list(params, method=method, language=language)
        records = extract_product_records(response)
        pages += 1
        if not records:
            break

        for record in records:
            skc_name = optional_text(first_value(record, "skcName", "skc_name"))
            if not skc_name:
                continue
            sku_code_list = string_list(first_value(record, "skuCodeList", "sku_code_list"))
            sku_codes_seen.update(sku_code_list)
            inserted = db.upsert_product(
                database_url,
                shop_key=credentials.shop_key,
                skc_name=skc_name,
                spu_name=optional_text(first_value(record, "spuName", "spu_name")),
                sku_code_list=sku_code_list,
                raw_payload=record,
            )
            products_seen += 1
            products_upserted += 1 if inserted else 0

        if len(records) < page_size:
            break

    return ProductSyncResult(pages, products_seen, products_upserted, len(sku_codes_seen))


def sync_product_details(
    *,
    database_url: str,
    credentials: StoreCredentials,
    query_params: dict[str, Any],
    method: str = "POST",
    page_start: int = 1,
    page_size: int = MAX_PRODUCT_SEARCH_PAGE_SIZE,
    max_pages: int = 1000,
    rps: float = 20.0,
    language: str = DEFAULT_LANGUAGE,
) -> ProductSyncResult:
    validate_page_options(
        query_params=query_params,
        page_start=page_start,
        page_size=page_size,
        max_pages=max_pages,
        rps=rps,
        max_page_size=MAX_PRODUCT_SEARCH_PAGE_SIZE,
        reserved_name="product detail sync",
    )
    db.init_db(database_url)
    service = ProductService(client_from_credentials(credentials))
    limiter = RateLimiter(rps)
    pages = 0
    products_seen = 0
    products_upserted = 0
    sku_codes_seen: set[str] = set()
    total_count: int | None = None

    for page_offset in range(max_pages):
        page_no = page_start + page_offset
        params = dict(query_params)
        params["pageNum"] = page_no
        params["pageSize"] = page_size

        limiter.wait()
        response = service.search_products(params, method=method, language=language)
        records = extract_product_records(response)
        total_count = extract_total_count(response) if total_count is None else total_count
        pages += 1
        if not records:
            break

        for record in records:
            spu_name = optional_text(first_value(record, "spuName", "spu_name"))
            if not spu_name:
                continue
            skc_list = record.get("skcList") if isinstance(record.get("skcList"), list) else []
            inserted = db.upsert_product_detail(
                database_url,
                shop_key=credentials.shop_key,
                spu_name=spu_name,
                spu_shelf_status=optional_int(first_value(record, "spuShelfStatus", "spu_shelf_status")),
                category_id=optional_text(first_value(record, "categoryId", "category_id")),
                skc_list=skc_list,
                raw_payload=record,
            )
            products_seen += 1
            products_upserted += 1 if inserted else 0
            for skc in skc_list:
                if not isinstance(skc, dict):
                    continue
                skc_name = optional_text(first_value(skc, "skcName", "skc_name"))
                if not skc_name:
                    continue
                sku_code_list = sku_codes_from_skc(skc)
                sku_codes_seen.update(sku_code_list)
                db.upsert_product(
                    database_url,
                    shop_key=credentials.shop_key,
                    skc_name=skc_name,
                    spu_name=spu_name,
                    sku_code_list=sku_code_list,
                    raw_payload={"spuName": spu_name, **skc},
                )

        if len(records) < page_size:
            break
        if total_count is not None and page_no * page_size >= total_count:
            break

    return ProductSyncResult(pages, products_seen, products_upserted, len(sku_codes_seen))


def validate_page_options(
    *,
    query_params: dict[str, Any],
    page_start: int,
    page_size: int,
    max_pages: int,
    rps: float,
    max_page_size: int,
    reserved_name: str,
) -> None:
    if page_start < 1:
        raise ValueError("page_start must be >= 1")
    if not 1 <= page_size <= max_page_size:
        raise ValueError(f"page_size must be between 1 and {max_page_size}")
    if max_pages < 1:
        raise ValueError("max_pages must be >= 1")
    if rps <= 0 or rps > MAX_PRODUCT_QUERY_RPS:
        raise ValueError(f"rps must be > 0 and <= {MAX_PRODUCT_QUERY_RPS:g}")
    reserved = {"pageNum", "pageSize"}
    conflicts = sorted(reserved.intersection(query_params))
    if conflicts:
        raise ValueError(f"params-json cannot include reserved {reserved_name} keys: {', '.join(conflicts)}")


def extract_product_records(response: dict[str, Any]) -> list[dict[str, Any]]:
    info = response.get("info")
    if isinstance(info, dict):
        for key in ("data", "productList", "list", "records", "rows"):
            value = info.get(key)
            if isinstance(value, list):
                return [item for item in value if isinstance(item, dict)]
    if isinstance(info, list):
        return [item for item in info if isinstance(item, dict)]
    return []


def extract_total_count(response: dict[str, Any]) -> int | None:
    info = response.get("info")
    if not isinstance(info, dict):
        return None
    meta = info.get("meta")
    count = meta.get("count") if isinstance(meta, dict) else info.get("count") or info.get("total")
    try:
        return int(count)
    except (TypeError, ValueError):
        return None


def sku_codes_from_skc(skc: dict[str, Any]) -> list[str]:
    sku_list = skc.get("skuList") if isinstance(skc.get("skuList"), list) else []
    return [
        sku_code
        for sku in sku_list
        if isinstance(sku, dict)
        for sku_code in [optional_text(first_value(sku, "skuCode", "sku_code"))]
        if sku_code
    ]


def first_value(payload: dict[str, Any], *keys: str) -> Any:
    for key in keys:
        if key in payload and payload[key] not in (None, ""):
            return payload[key]
    return None


def string_list(value: Any) -> list[str]:
    if not isinstance(value, list):
        return []
    return [str(item).strip() for item in value if str(item or "").strip()]


def optional_text(value: Any) -> str | None:
    if value in (None, ""):
        return None
    text = str(value).strip()
    return text or None


def optional_int(value: Any) -> int | None:
    if value in (None, ""):
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


class RateLimiter:
    def __init__(self, requests_per_second: float) -> None:
        self.interval_seconds = 0.0 if requests_per_second <= 0 else 1.0 / requests_per_second
        self.last_request_at = 0.0

    def wait(self) -> None:
        if self.interval_seconds <= 0:
            return
        now = time.monotonic()
        sleep_seconds = self.interval_seconds - (now - self.last_request_at)
        if sleep_seconds > 0:
            time.sleep(sleep_seconds)
        self.last_request_at = time.monotonic()
