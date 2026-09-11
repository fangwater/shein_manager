from __future__ import annotations

import json
import os
import urllib.error
import urllib.request
from typing import Any, Iterable


DEFAULT_XLWMS_API_BASE_URL = "http://127.0.0.1:18083/v1"


class PlatformMappingError(RuntimeError):
    pass


def xlwms_api_base_url() -> str:
    return os.getenv("XLWMS_BASE_URL", DEFAULT_XLWMS_API_BASE_URL).strip().rstrip("/")


def resolve_platform_skus(
    platform: str,
    platform_skus: Iterable[str],
    *,
    timeout_seconds: float = 15.0,
) -> dict[str, dict[str, Any]]:
    platform = platform.strip().lower()
    values = list(dict.fromkeys(str(value or "").strip() for value in platform_skus if str(value or "").strip()))
    if not platform:
        raise ValueError("platform is required")
    resolved: dict[str, dict[str, Any]] = {}
    for start in range(0, len(values), 500):
        payload = json.dumps(
            {"platform": platform, "platform_skus": values[start : start + 500]},
            separators=(",", ":"),
        ).encode("utf-8")
        request = urllib.request.Request(
            f"{xlwms_api_base_url()}/platform-sku-mappings/resolve",
            data=payload,
            headers={"Accept": "application/json", "Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=timeout_seconds) as response:
                body = json.load(response)
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
            raise PlatformMappingError("XLWMS platform SKU mapping query failed") from exc
        if not isinstance(body, dict) or not body.get("success") or not isinstance(body.get("data"), dict):
            message = str(body.get("error") or "XLWMS returned an invalid platform SKU mapping response") if isinstance(body, dict) else "XLWMS returned an invalid platform SKU mapping response"
            raise PlatformMappingError(message)
        for mapping in body["data"].get("mappings") or []:
            if not isinstance(mapping, dict):
                continue
            sku = str(mapping.get("platform_sku") or "").strip()
            if sku:
                resolved[sku] = mapping
    return resolved


def recipe_key(mapping: dict[str, Any]) -> tuple[tuple[str, int], ...]:
    items: list[tuple[str, int]] = []
    for item in mapping.get("items") or []:
        if not isinstance(item, dict):
            continue
        warehouse_sku = str(item.get("warehouse_sku") or "").strip()
        try:
            quantity = int(item.get("quantity") or 0)
        except (TypeError, ValueError):
            quantity = 0
        if warehouse_sku and quantity > 0:
            items.append((warehouse_sku, quantity))
    return tuple(sorted(items))
