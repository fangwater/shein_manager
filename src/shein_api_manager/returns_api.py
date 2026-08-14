from __future__ import annotations

from dataclasses import dataclass
import time
from typing import Any, Iterable

from . import db
from .client import SheinClient, StoreCredentials


RETURN_ORDER_LIST_PATH = "/open-api/return-order/list"
RETURN_ORDER_DETAIL_PATH = "/open-api/return-order/details"
MAX_LIST_PAGE_SIZE = 30
MAX_DETAIL_BATCH_SIZE = 30

RETURN_ORDER_STATUS_LABELS = {
    "1": "已关闭",
    "2": "已申请",
    "3": "已取消",
    "5": "已收货",
    "6": "已妥投",
    "7": "待交接",
    "8": "待SHEIN仓中转",
    "9": "已完成",
}


@dataclass(frozen=True)
class ReturnSyncResult:
    list_pages: int
    list_returns_seen: int
    list_returns_upserted: int
    return_order_nos: int
    detail_batches: int
    details_seen: int
    details_upserted: int


class ReturnService:
    def __init__(
        self,
        client: SheinClient,
        *,
        order_list_path: str = RETURN_ORDER_LIST_PATH,
        order_detail_path: str = RETURN_ORDER_DETAIL_PATH,
    ) -> None:
        self.client = client
        self.order_list_path = order_list_path
        self.order_detail_path = order_detail_path

    def order_list(self, params: dict[str, Any], *, method: str = "POST") -> dict[str, Any]:
        method = method.upper()
        if method == "GET":
            return self.client.request("GET", self.order_list_path, params=params)
        return self.client.request(method, self.order_list_path, json_body=params)

    def order_details(self, return_order_nos: list[str], *, method: str = "POST") -> dict[str, Any]:
        method = method.upper()
        body = {"returnOrderNoList": return_order_nos}
        if method == "GET":
            return self.client.request("GET", self.order_detail_path, params=body)
        return self.client.request(method, self.order_detail_path, json_body=body)


def client_from_credentials(credentials: StoreCredentials) -> SheinClient:
    return SheinClient(
        base_url=credentials.base_url,
        open_key_id=credentials.open_key_id,
        secret_key=credentials.secret_key,
    )


def sync_order_returns(
    *,
    database_url: str,
    credentials: StoreCredentials,
    query_params: dict[str, Any],
    order_list_path: str = RETURN_ORDER_LIST_PATH,
    order_detail_path: str = RETURN_ORDER_DETAIL_PATH,
    list_method: str = "POST",
    detail_method: str = "POST",
    page_start: int = 1,
    page_size: int = MAX_LIST_PAGE_SIZE,
    max_pages: int = 100,
    list_rps: float = 1.0,
    detail_rps: float = 1.0,
    fetch_details: bool = True,
) -> ReturnSyncResult:
    validate_sync_options(query_params=query_params, page_size=page_size, max_pages=max_pages)
    db.init_db(database_url, shop_key=credentials.shop_key)
    database_url = db.shop_database_url(database_url, credentials.shop_key)
    service = ReturnService(
        client_from_credentials(credentials),
        order_list_path=order_list_path,
        order_detail_path=order_detail_path,
    )
    list_limiter = RateLimiter(list_rps)
    detail_limiter = RateLimiter(detail_rps)
    page_size = min(page_size, MAX_LIST_PAGE_SIZE)

    list_pages = 0
    list_returns_seen = 0
    list_returns_upserted = 0
    return_order_nos: list[str] = []
    total_count: int | None = None

    for page_offset in range(max_pages):
        page_no = page_start + page_offset
        params = dict(query_params)
        params["page"] = page_no
        params["pageSize"] = page_size
        list_limiter.wait()
        response = service.order_list(params, method=list_method)
        records = extract_list_records(response)
        total_count = extract_total_count(response) if total_count is None else total_count
        list_pages += 1
        if not records:
            break
        for record in records:
            return_no = optional_text(record.get("returnOrderNo"))
            if not return_no:
                continue
            return_order_nos.append(return_no)
            inserted = db.upsert_order_return(
                database_url,
                shop_key=credentials.shop_key,
                return_no=return_no,
                order_no=optional_text(record.get("orderNo")),
                return_status=normalize_code(record.get("returnOrderStatus")),
                return_status_label=label(RETURN_ORDER_STATUS_LABELS, record.get("returnOrderStatus")),
                no_return_goods_sign=normalize_code(record.get("noReturnGoodsSign")),
                return_order_tag_code=normalize_code(record.get("returnOrderTagCode")),
                site=optional_text(record.get("site")),
                platform_express_no=optional_text(record.get("platformExpressNo")),
                member_express_no=optional_text(record.get("memberExpressNo")),
                express_company_name=optional_text(record.get("expressCompanyName")),
                performance_cost=record.get("performanceCost"),
                invoice_status=normalize_code(record.get("invoiceStatus")),
                request_return_time=optional_text(record.get("requestReturnTime")),
                allocate_time=optional_text(record.get("allocateTime")),
                last_update_time=optional_text(record.get("lastUpdateTime") or record.get("updateTime")),
                seller_signed_time=optional_text(record.get("sellerSignedTime")),
                cancel_time=optional_text(record.get("cancelTime")),
                completed_time=optional_text(record.get("completedTime")),
                check_status=normalize_code(record.get("checkStatus")),
                stock_mode=normalize_code(record.get("stockMode")),
                receive_type=normalize_code(record.get("receiveType")),
                refund_order_nos=string_list(record.get("refundOrderNos")),
                return_goods_info_list=record.get("returnGoodsInfoList") if isinstance(record.get("returnGoodsInfoList"), list) else [],
                raw_payload=record,
            )
            list_returns_seen += 1
            list_returns_upserted += 1 if inserted else 0
        if len(records) < page_size:
            break
        if total_count is not None and page_no * page_size >= total_count:
            break

    unique_return_order_nos = sorted(set(return_order_nos))
    detail_batches = 0
    details_seen = 0
    details_upserted = 0
    if fetch_details and unique_return_order_nos:
        for batch in chunked(unique_return_order_nos, MAX_DETAIL_BATCH_SIZE):
            detail_limiter.wait()
            response = service.order_details(batch, method=detail_method)
            detail_batches += 1
            for record in extract_detail_records(response):
                return_no = optional_text(record.get("returnOrderNo"))
                if not return_no:
                    continue
                inserted = db.upsert_order_return(
                    database_url,
                    shop_key=credentials.shop_key,
                    return_no=return_no,
                    order_no=optional_text(record.get("orderNo")),
                    return_status=normalize_code(record.get("returnOrderStatus")),
                    return_status_label=label(RETURN_ORDER_STATUS_LABELS, record.get("returnOrderStatus")),
                    no_return_goods_sign=normalize_code(record.get("noReturnGoodsSign")),
                    return_order_tag_code=normalize_code(record.get("returnOrderTagCode")),
                    site=optional_text(record.get("site")),
                    platform_express_no=optional_text(record.get("platformExpressNo")),
                    member_express_no=optional_text(record.get("memberExpressNo")),
                    express_company_name=optional_text(record.get("expressCompanyName")),
                    performance_cost=record.get("performanceCost"),
                    invoice_status=normalize_code(record.get("invoiceStatus")),
                    request_return_time=optional_text(record.get("requestReturnTime")),
                    allocate_time=optional_text(record.get("allocateTime")),
                    last_update_time=optional_text(record.get("lastUpdateTime") or record.get("updateTime")),
                    seller_signed_time=optional_text(record.get("sellerSignedTime")),
                    cancel_time=optional_text(record.get("cancelTime")),
                    completed_time=optional_text(record.get("completedTime")),
                    check_status=normalize_code(record.get("checkStatus")),
                    stock_mode=normalize_code(record.get("stockMode")),
                    receive_type=normalize_code(record.get("receiveType")),
                    refund_order_nos=string_list(record.get("refundOrderNos")),
                    return_goods_info_list=record.get("returnGoodsInfoList") if isinstance(record.get("returnGoodsInfoList"), list) else [],
                    raw_payload=record,
                )
                details_seen += 1
                details_upserted += 1 if inserted else 0

    return ReturnSyncResult(
        list_pages=list_pages,
        list_returns_seen=list_returns_seen,
        list_returns_upserted=list_returns_upserted,
        return_order_nos=len(unique_return_order_nos),
        detail_batches=detail_batches,
        details_seen=details_seen,
        details_upserted=details_upserted,
    )


def validate_sync_options(*, query_params: dict[str, Any], page_size: int, max_pages: int) -> None:
    if not 1 <= page_size <= MAX_LIST_PAGE_SIZE:
        raise ValueError(f"page_size must be between 1 and {MAX_LIST_PAGE_SIZE}")
    if max_pages < 1:
        raise ValueError("max_pages must be >= 1")
    required = ["startTime", "endTime", "queryType"]
    missing = [key for key in required if not query_params.get(key)]
    if missing:
        raise ValueError("return order list query requires startTime, endTime, and queryType")


def extract_list_records(response: dict[str, Any]) -> list[dict[str, Any]]:
    info = response.get("info")
    if isinstance(info, dict):
        for key in ("returnOrderList", "data", "list", "records", "rows"):
            value = info.get(key)
            if isinstance(value, list):
                return [item for item in value if isinstance(item, dict)]
    if isinstance(info, list):
        return [item for item in info if isinstance(item, dict)]
    return []


def extract_detail_records(response: dict[str, Any]) -> list[dict[str, Any]]:
    info = response.get("info")
    if isinstance(info, list):
        return [item for item in info if isinstance(item, dict)]
    return []


def extract_total_count(response: dict[str, Any]) -> int | None:
    info = response.get("info")
    if not isinstance(info, dict):
        return None
    meta = info.get("meta")
    if isinstance(meta, dict):
        count = meta.get("count")
    else:
        count = info.get("total") or info.get("totalCount") or info.get("count")
    try:
        return int(count)
    except (TypeError, ValueError):
        return None


def string_list(value: Any) -> list[str]:
    if not isinstance(value, list):
        return []
    return [str(item) for item in value if item not in (None, "")]


def normalize_code(value: Any) -> str | None:
    if value in (None, ""):
        return None
    try:
        return str(int(value))
    except (TypeError, ValueError):
        return str(value).strip() or None


def label(labels: dict[str, str], value: Any) -> str | None:
    code = normalize_code(value)
    if code is None:
        return None
    return labels.get(code, f"未知({code})")


def optional_text(value: Any) -> str | None:
    if value in (None, ""):
        return None
    return str(value)


def chunked(values: list[str], size: int) -> Iterable[list[str]]:
    for index in range(0, len(values), size):
        yield values[index : index + size]


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
