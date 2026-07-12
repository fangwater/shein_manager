from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timedelta
import hashlib
import json
import time
from typing import Any, Iterable

from . import db
from .client import SheinClient, StoreCredentials
from .order_statuses import order_status_label, order_status_normalized
from .order_types import normalize_order_type, order_type_label


ORDER_LIST_PATH = "/open-api/order/order-list"
ORDER_DETAIL_PATH = "/open-api/order/order-detail"
SHEIN_TIME_FORMAT = "%Y-%m-%d %H:%M:%S"
MAX_ORDER_LIST_RPS = 100.0
MAX_ORDER_DETAIL_RPS = 300.0
MAX_ORDER_LIST_WINDOW_HOURS = 48.0
MAX_ORDER_LIST_PAGE_SIZE = 30

ORDER_NO_KEYS = (
    "orderNo",
    "order_no",
    "orderSn",
    "order_sn",
    "orderId",
    "order_id",
    "billno",
    "billNo",
    "orderCode",
    "order_code",
)
STATUS_KEYS = ("orderStatus", "order_status", "status", "newGoodsStatus")
ORDER_TYPE_KEYS = ("orderType", "order_type")
CREATED_KEYS = ("createTime", "createdTime", "orderCreateTime", "order_created_at")
UPDATED_KEYS = ("updateTime", "updatedTime", "orderUpdateTime", "order_updated_at")
LIST_KEYS = ("list", "data", "records", "rows", "orderList", "order_list", "orders")
TOTAL_KEYS = ("total", "totalCount", "total_count", "count", "recordCount")


@dataclass(frozen=True)
class DownloadResult:
    run_id: int
    pages: int
    orders_seen: int
    orders_upserted: int


@dataclass(frozen=True)
class FullSyncResult:
    job_id: int
    sync_key: str
    status: str
    windows_total: int
    windows_completed: int
    pages: int
    orders_seen: int
    orders_upserted: int
    details_fetched: int


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


class OrderService:
    def __init__(self, client: SheinClient) -> None:
        self.client = client

    def order_list(self, params: dict[str, Any], *, method: str = "POST") -> dict[str, Any]:
        method = method.upper()
        if method == "GET":
            return self.client.request("GET", ORDER_LIST_PATH, params=params)
        return self.client.request(method, ORDER_LIST_PATH, json_body=params)

    def order_detail(
        self,
        order_nos: list[str],
        *,
        method: str = "POST",
        body_field: str = "orderNoList",
    ) -> dict[str, Any]:
        method = method.upper()
        body = {body_field: order_nos}
        if method == "GET":
            return self.client.request("GET", ORDER_DETAIL_PATH, params=body)
        return self.client.request(method, ORDER_DETAIL_PATH, json_body=body)


def client_from_credentials(credentials: StoreCredentials) -> SheinClient:
    return SheinClient(
        base_url=credentials.base_url,
        open_key_id=credentials.open_key_id,
        secret_key=credentials.secret_key,
    )


def download_orders(
    *,
    database_url: str,
    credentials: StoreCredentials,
    query_params: dict[str, Any],
    list_method: str,
    page_param: str,
    page_size_param: str,
    page_start: int,
    page_size: int,
    max_pages: int,
    fetch_details: bool,
    detail_method: str,
    detail_body_field: str,
) -> DownloadResult:
    service = OrderService(client_from_credentials(credentials))
    run_id = db.create_sync_run(database_url, shop_key=credentials.shop_key, query_params=query_params)
    pages = 0
    orders_seen = 0
    orders_upserted = 0
    try:
        for page_offset in range(max_pages):
            page_no = page_start + page_offset
            params = dict(query_params)
            params[page_param] = page_no
            params[page_size_param] = page_size

            response = service.order_list(params, method=list_method)
            records = extract_records(response)
            pages += 1
            if not records:
                break

            order_nos: list[str] = []
            for record in records:
                if not isinstance(record, dict):
                    continue
                order_no = extract_first(record, ORDER_NO_KEYS)
                if not order_no:
                    continue
                order_no = str(order_no)
                order_nos.append(order_no)
                raw_order_status = _to_optional_text(extract_first(record, STATUS_KEYS))
                raw_order_type = normalize_order_type(extract_first(record, ORDER_TYPE_KEYS))
                inserted = db.upsert_order(
                    database_url,
                    shop_key=credentials.shop_key,
                    order_no=order_no,
                    list_payload=record,
                    order_status=raw_order_status,
                    order_status_label=order_status_label(raw_order_status) if raw_order_status else None,
                    order_status_normalized=order_status_normalized(raw_order_status),
                    order_type=raw_order_type,
                    order_type_label=order_type_label(raw_order_type) if raw_order_type else None,
                    order_created_at=_to_optional_text(extract_first(record, CREATED_KEYS)),
                    order_updated_at=_to_optional_text(extract_first(record, UPDATED_KEYS)),
                )
                orders_seen += 1
                orders_upserted += 1 if inserted else 0

            if fetch_details and order_nos:
                for batch in chunked(order_nos, 30):
                    detail_response = service.order_detail(
                        batch,
                        method=detail_method,
                        body_field=detail_body_field,
                    )
                    update_order_details_from_response(
                        database_url=database_url,
                        shop_key=credentials.shop_key,
                        order_nos=batch,
                        detail_response=detail_response,
                    )

            if len(records) < page_size:
                break

        db.finish_sync_run(
            database_url,
            run_id=run_id,
            status="success",
            pages=pages,
            orders_seen=orders_seen,
            orders_upserted=orders_upserted,
        )
        return DownloadResult(run_id, pages, orders_seen, orders_upserted)
    except Exception as exc:
        db.finish_sync_run(
            database_url,
            run_id=run_id,
            status="failed",
            pages=pages,
            orders_seen=orders_seen,
            orders_upserted=orders_upserted,
            error=str(exc),
        )
        raise


def sync_orders_full(
    *,
    database_url: str,
    credentials: StoreCredentials,
    start_time: str,
    end_time: str,
    query_type: int,
    extra_params: dict[str, Any],
    sync_key: str | None,
    list_method: str,
    detail_method: str,
    detail_body_field: str,
    page_size: int = MAX_ORDER_LIST_PAGE_SIZE,
    window_hours: float = MAX_ORDER_LIST_WINDOW_HOURS,
    list_rps: float = 20.0,
    detail_rps: float = 50.0,
    reset: bool = False,
) -> FullSyncResult:
    validate_full_sync_options(
        start_time=start_time,
        end_time=end_time,
        query_type=query_type,
        page_size=page_size,
        window_hours=window_hours,
        list_rps=list_rps,
        detail_rps=detail_rps,
        extra_params=extra_params,
    )
    start_dt = parse_shein_time(start_time)
    end_dt = parse_shein_time(end_time)
    windows = build_time_windows(start_dt, end_dt, window_hours=window_hours)
    base_params = dict(extra_params)
    sync_key = sync_key or build_sync_key(
        shop_key=credentials.shop_key,
        query_type=query_type,
        start_time=start_time,
        end_time=end_time,
        base_params=base_params,
    )

    db.init_db(database_url)
    job_id = db.ensure_order_full_sync_job(
        database_url,
        shop_key=credentials.shop_key,
        sync_key=sync_key,
        query_type=query_type,
        start_time=start_time,
        end_time=end_time,
        base_params=base_params,
        window_hours=window_hours,
        list_rps=list_rps,
        detail_rps=detail_rps,
        reset=reset,
    )
    db.ensure_order_full_sync_windows(database_url, job_id=job_id, windows=windows)

    service = OrderService(client_from_credentials(credentials))
    list_limiter = RateLimiter(list_rps)
    detail_limiter = RateLimiter(detail_rps)

    try:
        while True:
            pending_windows = db.get_pending_order_full_sync_windows(database_url, job_id=job_id)
            if not pending_windows:
                break
            for window in pending_windows:
                db.start_order_full_sync_window(database_url, window_id=window.id)
                page_no = max(window.next_page, 1)
                while True:
                    params = dict(base_params)
                    params.update(
                        {
                            "queryType": query_type,
                            "startTime": window.start_time,
                            "endTime": window.end_time,
                            "page": page_no,
                            "pageSize": page_size,
                        }
                    )

                    list_limiter.wait()
                    response = service.order_list(params, method=list_method)
                    records = extract_records(response)
                    if not records:
                        db.finish_order_full_sync_window(database_url, window_id=window.id, status="success")
                        break

                    order_nos: list[str] = []
                    orders_seen_this_page = 0
                    orders_upserted_this_page = 0
                    for record in records:
                        order_no = extract_first(record, ORDER_NO_KEYS)
                        if not order_no:
                            continue
                        order_no = str(order_no)
                        order_nos.append(order_no)
                        raw_order_status = _to_optional_text(extract_first(record, STATUS_KEYS))
                        raw_order_type = normalize_order_type(extract_first(record, ORDER_TYPE_KEYS))
                        inserted = db.upsert_order(
                            database_url,
                            shop_key=credentials.shop_key,
                            order_no=order_no,
                            list_payload=record,
                            order_status=raw_order_status,
                            order_status_label=order_status_label(raw_order_status) if raw_order_status else None,
                            order_status_normalized=order_status_normalized(raw_order_status),
                            order_type=raw_order_type,
                            order_type_label=order_type_label(raw_order_type) if raw_order_type else None,
                            order_created_at=_to_optional_text(extract_first(record, CREATED_KEYS)),
                            order_updated_at=_to_optional_text(extract_first(record, UPDATED_KEYS)),
                        )
                        orders_seen_this_page += 1
                        orders_upserted_this_page += 1 if inserted else 0

                    details_fetched_this_page = 0
                    detail_batches_this_page = 0
                    for batch in chunked(order_nos, 30):
                        detail_limiter.wait()
                        detail_response = service.order_detail(
                            batch,
                            method=detail_method,
                            body_field=detail_body_field,
                        )
                        update_order_details_from_response(
                            database_url=database_url,
                            shop_key=credentials.shop_key,
                            order_nos=batch,
                            detail_response=detail_response,
                        )
                        details_fetched_this_page += len(batch)
                        detail_batches_this_page += 1

                    db.complete_order_full_sync_window_page(
                        database_url,
                        window_id=window.id,
                        next_page=page_no + 1,
                        orders_seen_delta=orders_seen_this_page,
                        orders_upserted_delta=orders_upserted_this_page,
                        details_fetched_delta=details_fetched_this_page,
                        detail_batches_delta=detail_batches_this_page,
                    )

                    if len(records) < page_size:
                        db.finish_order_full_sync_window(database_url, window_id=window.id, status="success")
                        break
                    if page_no * page_size >= 9990:
                        continuation_start = next_window_start_from_records(records, query_type=query_type)
                        if continuation_start and continuation_start < window.end_time:
                            db.create_order_full_sync_continuation_window(
                                database_url,
                                job_id=job_id,
                                start_time=continuation_start,
                                end_time=window.end_time,
                            )
                        db.finish_order_full_sync_window(database_url, window_id=window.id, status="success")
                        break
                    page_no += 1

        summary = db.get_order_full_sync_summary(database_url, job_id=job_id)
        status = "success" if summary["windowsCompleted"] == summary["windowsTotal"] else "running"
        db.finish_order_full_sync_job(database_url, job_id=job_id, status=status)
        summary = db.get_order_full_sync_summary(database_url, job_id=job_id)
        return full_sync_result_from_summary(summary)
    except Exception as exc:
        failed_window = locals().get("window")
        if failed_window is not None:
            db.finish_order_full_sync_window(
                database_url,
                window_id=failed_window.id,
                status="failed",
                error=str(exc),
            )
        db.finish_order_full_sync_job(database_url, job_id=job_id, status="failed", error=str(exc))
        raise


def update_order_details_from_response(
    *,
    database_url: str,
    shop_key: str,
    order_nos: list[str],
    detail_response: dict[str, Any],
) -> None:
    detail_records = extract_records(detail_response)
    detail_by_order = map_details_to_order(detail_records)
    for order_no in order_nos:
        detail_payload = detail_by_order.get(order_no, detail_response)
        raw_order_status = _to_optional_text(extract_first(detail_payload, STATUS_KEYS))
        raw_order_type = normalize_order_type(extract_first(detail_payload, ORDER_TYPE_KEYS))
        db.update_order_detail(
            database_url,
            shop_key=shop_key,
            order_no=order_no,
            detail_payload=detail_payload,
            order_status=raw_order_status,
            order_status_label=order_status_label(raw_order_status) if raw_order_status else None,
            order_status_normalized=order_status_normalized(raw_order_status),
            order_type=raw_order_type,
            order_type_label=order_type_label(raw_order_type) if raw_order_type else None,
        )


def validate_full_sync_options(
    *,
    start_time: str,
    end_time: str,
    query_type: int,
    page_size: int,
    window_hours: float,
    list_rps: float,
    detail_rps: float,
    extra_params: dict[str, Any],
) -> None:
    start_dt = parse_shein_time(start_time)
    end_dt = parse_shein_time(end_time)
    if start_dt > end_dt:
        raise ValueError("start_time must be earlier than or equal to end_time")
    if query_type not in (1, 2):
        raise ValueError("query_type must be 1 or 2")
    if not 1 <= page_size <= MAX_ORDER_LIST_PAGE_SIZE:
        raise ValueError(f"page_size must be between 1 and {MAX_ORDER_LIST_PAGE_SIZE}")
    if window_hours <= 0 or window_hours > MAX_ORDER_LIST_WINDOW_HOURS:
        raise ValueError(f"window_hours must be > 0 and <= {MAX_ORDER_LIST_WINDOW_HOURS}")
    if list_rps <= 0 or list_rps > MAX_ORDER_LIST_RPS:
        raise ValueError(f"list_rps must be > 0 and <= {MAX_ORDER_LIST_RPS}")
    if detail_rps <= 0 or detail_rps > MAX_ORDER_DETAIL_RPS:
        raise ValueError(f"detail_rps must be > 0 and <= {MAX_ORDER_DETAIL_RPS}")
    reserved = {"queryType", "startTime", "endTime", "page", "pageSize"}
    conflicts = sorted(reserved.intersection(extra_params))
    if conflicts:
        raise ValueError(f"params-json cannot include reserved full-sync keys: {', '.join(conflicts)}")


def parse_shein_time(value: str) -> datetime:
    try:
        return datetime.strptime(value, SHEIN_TIME_FORMAT)
    except ValueError as exc:
        raise ValueError(f"time must match {SHEIN_TIME_FORMAT}: {value}") from exc


def format_shein_time(value: datetime) -> str:
    return value.strftime(SHEIN_TIME_FORMAT)


def build_time_windows(start_dt: datetime, end_dt: datetime, *, window_hours: float) -> list[tuple[int, str, str]]:
    windows: list[tuple[int, str, str]] = []
    cursor = start_dt
    step = timedelta(hours=window_hours)
    index = 1
    while cursor <= end_dt:
        window_end = min(cursor + step, end_dt)
        windows.append((index, format_shein_time(cursor), format_shein_time(window_end)))
        cursor = window_end + timedelta(seconds=1)
        index += 1
    return windows


def next_window_start_from_records(records: list[dict[str, Any]], *, query_type: int) -> str | None:
    key_candidates = CREATED_KEYS if query_type == 1 else UPDATED_KEYS
    latest: datetime | None = None
    for record in records:
        raw = extract_first(record, key_candidates)
        if not raw:
            continue
        try:
            parsed = parse_shein_time(str(raw))
        except ValueError:
            continue
        if latest is None or parsed > latest:
            latest = parsed
    if latest is None:
        return None
    return format_shein_time(latest)


def build_sync_key(
    *,
    shop_key: str,
    query_type: int,
    start_time: str,
    end_time: str,
    base_params: dict[str, Any],
) -> str:
    payload = json.dumps(
        {
            "shopKey": shop_key,
            "queryType": query_type,
            "startTime": start_time,
            "endTime": end_time,
            "baseParams": base_params,
        },
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    )
    digest = hashlib.sha256(payload.encode("utf-8")).hexdigest()[:16]
    return f"orders-full-{digest}"


def full_sync_result_from_summary(summary: dict[str, Any]) -> FullSyncResult:
    return FullSyncResult(
        job_id=int(summary["jobId"]),
        sync_key=str(summary["syncKey"]),
        status=str(summary["status"]),
        windows_total=int(summary["windowsTotal"]),
        windows_completed=int(summary["windowsCompleted"]),
        pages=int(summary["pages"]),
        orders_seen=int(summary["ordersSeen"]),
        orders_upserted=int(summary["ordersInserted"]),
        details_fetched=int(summary["detailsFetched"]),
    )


def extract_records(response: dict[str, Any]) -> list[dict[str, Any]]:
    info = response.get("info")
    if isinstance(info, list):
        return [item for item in info if isinstance(item, dict)]
    if isinstance(info, dict):
        found = _find_first_list(info)
        if found is not None:
            return [item for item in found if isinstance(item, dict)]
    found = _find_first_list(response)
    if found is not None:
        return [item for item in found if isinstance(item, dict)]
    return []


def _find_first_list(value: Any) -> list[Any] | None:
    if isinstance(value, dict):
        for key in LIST_KEYS:
            candidate = value.get(key)
            if isinstance(candidate, list):
                return candidate
        for candidate in value.values():
            found = _find_first_list(candidate)
            if found is not None:
                return found
    return None


def extract_first(payload: dict[str, Any], keys: Iterable[str]) -> Any:
    for key in keys:
        if key in payload and payload[key] not in (None, ""):
            return payload[key]
    for value in payload.values():
        if isinstance(value, dict):
            found = extract_first(value, keys)
            if found not in (None, ""):
                return found
    return None


def map_details_to_order(detail_records: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    mapped: dict[str, dict[str, Any]] = {}
    for record in detail_records:
        order_no = extract_first(record, ORDER_NO_KEYS)
        if order_no:
            mapped[str(order_no)] = record
    return mapped


def chunked(values: list[str], size: int) -> Iterable[list[str]]:
    for index in range(0, len(values), size):
        yield values[index : index + size]


def _to_optional_text(value: Any) -> str | None:
    if value in (None, ""):
        return None
    return str(value)
