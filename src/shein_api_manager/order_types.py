from __future__ import annotations

from typing import Any


ORDER_TYPE_LABELS = {
    "1": "正常订单",
    "2": "换货订单",
    "4": "认证仓转自发货订单",
    "5": "认证仓订单",
}


def normalize_order_type(value: Any) -> str | None:
    if value in (None, ""):
        return None
    try:
        return str(int(value))
    except (TypeError, ValueError):
        return str(value).strip() or None


def order_type_label(value: Any) -> str:
    normalized = normalize_order_type(value)
    if normalized is None:
        return "未知订单类型"
    return ORDER_TYPE_LABELS.get(normalized, f"未知订单类型({normalized})")
