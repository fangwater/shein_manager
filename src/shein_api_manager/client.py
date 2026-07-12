from __future__ import annotations

import json
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any

from .crypto import build_signature, decrypt_secret_key


class SheinApiError(RuntimeError):
    def __init__(self, message: str, *, status: int | None = None, payload: Any = None) -> None:
        super().__init__(message)
        self.status = status
        self.payload = payload


@dataclass(frozen=True)
class StoreCredentials:
    shop_key: str
    open_key_id: str
    secret_key: str
    base_url: str


class SheinClient:
    def __init__(
        self,
        *,
        base_url: str,
        open_key_id: str | None = None,
        secret_key: str | None = None,
        app_id: str | None = None,
        app_secret_key: str | None = None,
        timeout_seconds: int = 30,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.open_key_id = open_key_id
        self.secret_key = secret_key
        self.app_id = app_id
        self.app_secret_key = app_secret_key
        self.timeout_seconds = timeout_seconds

    def _auth_headers(self, path: str, *, use_app_auth: bool, language: str | None = None) -> dict[str, str]:
        timestamp_ms = str(int(time.time() * 1000))
        headers = {
            "Content-Type": "application/json;charset=UTF-8",
            "Accept": "application/json",
            "x-lt-timestamp": timestamp_ms,
        }
        if language:
            headers["language"] = language
        if use_app_auth:
            if not self.app_id or not self.app_secret_key:
                raise SheinApiError("SHEIN_APP_ID and SHEIN_APP_SECRET_KEY are required")
            headers["x-lt-appid"] = self.app_id
            headers["x-lt-signature"] = build_signature(self.app_id, self.app_secret_key, path, timestamp_ms)
            return headers

        if not self.open_key_id or not self.secret_key:
            raise SheinApiError("SHEIN_OPEN_KEY_ID and SHEIN_SECRET_KEY are required")
        headers["x-lt-openKeyId"] = self.open_key_id
        headers["x-lt-signature"] = build_signature(self.open_key_id, self.secret_key, path, timestamp_ms)
        return headers

    def request(
        self,
        method: str,
        path: str,
        *,
        params: dict[str, Any] | None = None,
        json_body: dict[str, Any] | None = None,
        use_app_auth: bool = False,
        require_success_code: bool = True,
        language: str | None = None,
    ) -> dict[str, Any]:
        method = method.upper()
        query = ""
        if params:
            query = "?" + urllib.parse.urlencode(params, doseq=True)
        url = self.base_url + path + query
        body = None
        if json_body is not None:
            body = json.dumps(json_body, separators=(",", ":"), ensure_ascii=False).encode("utf-8")

        request = urllib.request.Request(
            url,
            data=body,
            headers=self._auth_headers(path, use_app_auth=use_app_auth, language=language),
            method=method,
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout_seconds) as response:
                response_body = response.read().decode("utf-8")
                status = response.status
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", errors="replace")
            payload = _parse_json(raw)
            raise SheinApiError(f"SHEIN HTTP {exc.code}: {raw}", status=exc.code, payload=payload) from exc
        except urllib.error.URLError as exc:
            raise SheinApiError(f"SHEIN request failed: {exc}") from exc

        payload = _parse_json(response_body)
        if not isinstance(payload, dict):
            raise SheinApiError(f"SHEIN returned non-object JSON: {response_body}", status=status, payload=payload)
        if require_success_code and str(payload.get("code")) != "0":
            raise SheinApiError(
                f"SHEIN API error code={payload.get('code')} msg={payload.get('msg')}",
                status=status,
                payload=payload,
            )
        return payload

    def exchange_temp_token(self, temp_token: str) -> dict[str, Any]:
        payload = self.request(
            "POST",
            "/open-api/auth/get-by-token",
            json_body={"tempToken": temp_token},
            use_app_auth=True,
        )
        info = payload.get("info") or {}
        encrypted_secret = info.get("secretKey")
        if encrypted_secret and self.app_secret_key:
            info["secretKey"] = decrypt_secret_key(encrypted_secret, self.app_secret_key)
        return payload


def _parse_json(raw: str) -> Any:
    if not raw:
        return {}
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        raise SheinApiError(f"Invalid JSON response: {raw}") from exc
