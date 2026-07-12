from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Iterable

from .order_statuses import order_status_normalized


RETURN_TIME_KEYS = (
    "orderReturnTime",
    "returnTime",
    "returnedTime",
    "refundTime",
    "returnCreatedTime",
    "returnCompletedTime",
    "afterSaleTime",
)

RETURN_KEY_TERMS = (
    "return",
    "refund",
    "aftersale",
    "after_sale",
    "after-sale",
    "退货",
    "退款",
    "售后",
)

NON_SIGNAL_KEY_TERMS = (
    "returnpolicy",
    "return_policy",
    "return-policy",
    "returnwindow",
    "return_window",
    "return-window",
    "returnable",
    "canreturn",
    "can_return",
    "allowreturn",
    "allow_return",
)

RETURN_TEXT_TERMS = (
    "return",
    "returned",
    "refund",
    "refunded",
    "after sale",
    "after-sale",
    "after_sale",
    "退货",
    "退款",
    "售后",
)

MAX_EVIDENCE = 20


@dataclass(frozen=True)
class ReturnInfo:
    detected: bool
    status: str
    order_return_time: str | None
    evidence: list[dict[str, Any]]


def detect_order_return(
    *,
    list_payload: dict[str, Any] | None = None,
    detail_payload: dict[str, Any] | None = None,
) -> ReturnInfo:
    payloads = [payload for payload in (detail_payload, list_payload) if isinstance(payload, dict)]
    evidence: list[dict[str, Any]] = []
    order_return_time: str | None = None

    for source, payload in (("detail", detail_payload), ("list", list_payload)):
        if not isinstance(payload, dict):
            continue
        if order_return_time is None:
            order_return_time = first_non_empty_value(payload, RETURN_TIME_KEYS)
        for item in find_return_evidence(payload, source=source):
            evidence.append(item)
            if len(evidence) >= MAX_EVIDENCE:
                break
        if len(evidence) >= MAX_EVIDENCE:
            break

    if order_return_time and not any(item.get("key") == "orderReturnTime" for item in evidence):
        evidence.insert(
            0,
            {
                "source": "detail" if isinstance(detail_payload, dict) else "list",
                "path": "orderReturnTime",
                "key": "orderReturnTime",
                "value": order_return_time,
            },
        )

    goods_return_count = 0
    goods_count = 0
    for payload in payloads:
        goods_list = payload.get("orderGoodsInfoList")
        if not isinstance(goods_list, list):
            continue
        for goods in goods_list:
            if not isinstance(goods, dict):
                continue
            goods_count += 1
            if detect_goods_return(goods).detected:
                goods_return_count += 1
        if goods_count:
            break

    detected = bool(order_return_time or evidence or goods_return_count)
    if not detected:
        status = "none"
    elif order_return_time:
        status = "returned"
    elif goods_return_count and goods_count and goods_return_count < goods_count:
        status = "partial_return"
    elif goods_return_count:
        status = "returned"
    else:
        status = "suspected_return"

    return ReturnInfo(
        detected=detected,
        status=status,
        order_return_time=order_return_time,
        evidence=evidence[:MAX_EVIDENCE],
    )



def normalize_order_status(raw_status: Any, return_status: str | None = None) -> str:
    return order_status_normalized(raw_status)


def detect_goods_return(goods: dict[str, Any]) -> ReturnInfo:
    evidence = list(find_return_evidence(goods, source="goods"))[:MAX_EVIDENCE]
    return ReturnInfo(
        detected=bool(evidence),
        status="suspected_return" if evidence else "none",
        order_return_time=first_non_empty_value(goods, RETURN_TIME_KEYS),
        evidence=evidence,
    )


def find_return_evidence(payload: Any, *, source: str) -> Iterable[dict[str, Any]]:
    yield from _find_return_evidence(payload, source=source, path="")


def first_non_empty_value(payload: Any, keys: Iterable[str]) -> str | None:
    if isinstance(payload, dict):
        for key in keys:
            if key in payload and is_signal_value(payload[key]):
                return stringify_value(payload[key])
        for value in payload.values():
            found = first_non_empty_value(value, keys)
            if found:
                return found
    elif isinstance(payload, list):
        for value in payload:
            found = first_non_empty_value(value, keys)
            if found:
                return found
    return None


def _find_return_evidence(payload: Any, *, source: str, path: str) -> Iterable[dict[str, Any]]:
    if isinstance(payload, dict):
        for key, value in payload.items():
            child_path = f"{path}.{key}" if path else str(key)
            if is_return_key(key) and is_signal_value(value):
                yield {
                    "source": source,
                    "path": child_path,
                    "key": str(key),
                    "value": stringify_value(value),
                }
            elif is_status_key(key) and is_return_text(value):
                yield {
                    "source": source,
                    "path": child_path,
                    "key": str(key),
                    "value": stringify_value(value),
                }
            yield from _find_return_evidence(value, source=source, path=child_path)
    elif isinstance(payload, list):
        for index, value in enumerate(payload):
            child_path = f"{path}[{index}]" if path else f"[{index}]"
            yield from _find_return_evidence(value, source=source, path=child_path)


def is_return_key(key: Any) -> bool:
    normalized = normalize_key(key)
    if any(term in normalized for term in NON_SIGNAL_KEY_TERMS):
        return False
    return any(term in normalized for term in RETURN_KEY_TERMS)


def is_status_key(key: Any) -> bool:
    normalized = normalize_key(key)
    return "status" in normalized or "reason" in normalized


def normalize_key(key: Any) -> str:
    return str(key or "").replace(" ", "").lower()


def is_return_text(value: Any) -> bool:
    if not isinstance(value, str):
        return False
    lowered = value.strip().lower()
    return bool(lowered) and any(term in lowered for term in RETURN_TEXT_TERMS)


def is_signal_value(value: Any) -> bool:
    if value is None:
        return False
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return value != 0
    if isinstance(value, str):
        stripped = value.strip()
        return stripped.lower() not in {"", "0", "false", "none", "null", "-", "n/a"}
    if isinstance(value, (list, tuple, set, dict)):
        return bool(value)
    return True


def stringify_value(value: Any) -> str:
    if isinstance(value, (dict, list)):
        return f"{type(value).__name__}({len(value)})"
    return str(value)
