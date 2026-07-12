from __future__ import annotations

import base64
import hashlib
import hmac
import secrets
from dataclasses import dataclass

from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes


DEFAULT_IV_SEED = "space-station-default-iv"
RANDOM_KEY_LENGTH = 5


@dataclass(frozen=True)
class Signature:
    timestamp_ms: str
    value: str


def _random_key(length: int = RANDOM_KEY_LENGTH) -> str:
    return secrets.token_hex(8)[:length]


def hmac_sha256_hex(message: str, secret: str) -> str:
    return hmac.new(secret.encode("utf-8"), message.encode("utf-8"), hashlib.sha256).hexdigest()


def base64_encode_text(data: str) -> str:
    return base64.b64encode(data.encode("utf-8")).decode("utf-8")


def build_signature(key_id: str, secret_key: str, url_path: str, timestamp_ms: str) -> str:
    """Build SHEIN x-lt-signature.

    Official examples sign: keyId & timestamp & urlPath, using secretKey + randomKey.
    The random key is prepended to the final base64-encoded HMAC hex digest.
    """
    random_key = _random_key()
    sign_string = f"{key_id}&{timestamp_ms}&{url_path}"
    digest_hex = hmac_sha256_hex(sign_string, secret_key + random_key)
    return random_key + base64_encode_text(digest_hex)


def _pkcs7_unpad(data: bytes) -> bytes:
    if not data:
        raise ValueError("empty decrypted data")
    pad_len = data[-1]
    if pad_len < 1 or pad_len > 16:
        raise ValueError("invalid PKCS7 padding")
    if data[-pad_len:] != bytes([pad_len]) * pad_len:
        raise ValueError("invalid PKCS7 padding bytes")
    return data[:-pad_len]


def decrypt_secret_key(encrypted_secret_key: str, app_secret_key: str) -> str:
    """Decrypt SHEIN get-by-token secretKey with appSecretKey.

    SHEIN examples use AES-128-CBC with the first 16 bytes of appSecretKey and
    IV seed "space-station-default-iv" truncated to 16 bytes.
    """
    key = app_secret_key.encode("utf-8")[:16].ljust(16, b"\0")
    iv = DEFAULT_IV_SEED.encode("utf-8")[:16]
    encrypted = base64.b64decode(encrypted_secret_key)
    decryptor = Cipher(algorithms.AES(key), modes.CBC(iv)).decryptor()
    padded = decryptor.update(encrypted) + decryptor.finalize()
    return _pkcs7_unpad(padded).decode("utf-8")
