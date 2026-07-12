from __future__ import annotations

from typing import Any


ORDER_STATUS_LABELS = {
    "1": "待处理",
    "2": "待发货",
    "4": "已发货",
    "5": "已签收",
    "6": "用户已退款",
    "7": "待揽收",
    "8": "已报损",
    "9": "已拒收",
}

ORDER_STATUS_NORMALIZED = {
    "1": "pending",
    "2": "pending_shipment",
    "4": "shipped",
    "5": "delivered",
    "6": "refunded",
    "7": "pending_pickup",
    "8": "reported_loss",
    "9": "rejected",
}


def normalize_order_status_code(value: Any) -> str | None:
    if value in (None, ""):
        return None
    try:
        return str(int(value))
    except (TypeError, ValueError):
        return str(value).strip() or None


def order_status_label(value: Any) -> str:
    normalized = normalize_order_status_code(value)
    if normalized is None:
        return "未知订单状态"
    return ORDER_STATUS_LABELS.get(normalized, f"未知订单状态({normalized})")


def order_status_normalized(value: Any) -> str:
    normalized = normalize_order_status_code(value)
    if normalized is None:
        return "unknown"
    return ORDER_STATUS_NORMALIZED.get(normalized, f"shein_status_{normalized}")
