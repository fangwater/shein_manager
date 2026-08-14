from __future__ import annotations

import os
from dataclasses import dataclass

try:
    from dotenv import load_dotenv
except ModuleNotFoundError:  # Allows CLI help before dependencies are installed.
    def load_dotenv(*_args: object, **_kwargs: object) -> bool:
        return False


@dataclass(frozen=True)
class Settings:
    database_url: str | None
    api_base_url: str
    auth_base_url: str
    app_id: str | None
    app_secret_key: str | None
    open_key_id: str | None
    secret_key: str | None
    shop_key: str
    order_list_method: str
    order_detail_method: str
    order_detail_field: str
    return_order_list_method: str
    return_order_list_path: str
    return_order_detail_method: str
    return_order_detail_path: str


def load_settings() -> Settings:
    load_dotenv()
    return Settings(
        database_url=os.getenv("DATABASE_URL"),
        api_base_url=os.getenv("SHEIN_API_BASE_URL", "https://openapi.sheincorp.com").rstrip("/"),
        auth_base_url=os.getenv("SHEIN_AUTH_BASE_URL", "https://openapi-sem.sheincorp.com").rstrip("/"),
        app_id=os.getenv("SHEIN_APP_ID") or None,
        app_secret_key=os.getenv("SHEIN_APP_SECRET_KEY") or None,
        open_key_id=os.getenv("SHEIN_OPEN_KEY_ID") or None,
        secret_key=os.getenv("SHEIN_SECRET_KEY") or None,
        shop_key=os.getenv("SHEIN_SHOP_KEY", "beauty-hangers-home"),
        order_list_method=os.getenv("SHEIN_ORDER_LIST_METHOD", "POST").upper(),
        order_detail_method=os.getenv("SHEIN_ORDER_DETAIL_METHOD", "POST").upper(),
        order_detail_field=os.getenv("SHEIN_ORDER_DETAIL_FIELD", "orderNoList"),
        return_order_list_method=os.getenv("SHEIN_RETURN_ORDER_LIST_METHOD", "POST").upper(),
        return_order_list_path=os.getenv(
            "SHEIN_RETURN_ORDER_LIST_PATH",
            "/open-api/return-order/list",
        ),
        return_order_detail_method=os.getenv("SHEIN_RETURN_ORDER_DETAIL_METHOD", "POST").upper(),
        return_order_detail_path=os.getenv(
            "SHEIN_RETURN_ORDER_DETAIL_PATH",
            "/open-api/return-order/details",
        ),
    )
