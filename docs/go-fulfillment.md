# SHEIN Go Manager

The service runs as a standalone Go process on `127.0.0.1:18084` and is exposed
through `https://pangutech.online/shein/`. It reads store credentials from the
existing `shein_shops` PostgreSQL table and never returns credential values.

## Local API

Use `X-Shein-Shop` to select the shop for every local API request. The
`shop_key` field remains available for API compatibility; when both are sent
they must match. `GET /api/system/shops` returns the configured shops and the
current/default selection. All POST endpoints accept this envelope:

```json
{
  "shop_key": "default",
  "data": {}
}
```

| Local endpoint | SHEIN upstream endpoint |
| --- | --- |
| `POST /api/order/list` | `POST /open-api/order/order-list` |
| `POST /api/order/detail` | `POST /open-api/order/order-detail` |
| `POST /api/order/export-address` | `POST /open-api/order/export-address` |
| `POST /api/shipping/warehouses` | `POST /open-api/gsp/available-shipping-warehouse` |
| `POST /api/shipping/channels` | `POST /open-api/gsp/order-mapping-channels` |
| `POST /api/shipping/place` | `POST /open-api/gsp/place-express-order` |
| `POST /api/shipping/check` | `POST /open-api/gsp/check-express-order` |
| `POST /api/shipping/label` | `POST /open-api/order/print-express-info` |
| `GET /api/shipping/track` | `GET /open-api/gsp/logistics-track` |

Forward tracking requires `orderNo` with either `packageNo` or `waybillNo`;
return tracking uses `returnOrderNo` by itself.

The production prefix is `/shein`, so `/api/order/list` is publicly routed as
`/shein/api/order/list`.

## Shop Fulfillment Console

The authenticated page at `/shein/` follows the same shop-level operating model
as the Temu console. Operators select a shop, query orders, select a SHEIN
shipping warehouse and channel, submit the online shipment, check its result,
and retrieve the label from the resulting task.

The selected shop is sent in `X-Shein-Shop` and kept in session storage. The
browser stores only normalized task references per shop: order number, channel
code, `placeRequestId`, `deliveryNo`, status, and update time. Customer
addresses and raw OpenAPI responses are not persisted in browser storage.

## Safety

`place-express-order`, `print-express-info`, and `export-address` with
`handleType=2` require an exact `X-Confirm-Shein-Action` header plus an
`Idempotency-Key`. Reusing the same key and request returns the stored result;
reusing it for different data is rejected.

All routes except `/healthz` require a valid existing `shein_pnl_session` cookie.
Address responses and all API responses use `Cache-Control: no-store`. Logs include
only the operation, shop, authenticated username, duration, error code, and trace ID.
They do not include request bodies, credentials, addresses, or order numbers.

## Runtime

```bash
mkdir -p bin
go build -o bin/shein-server ./cmd/server
pm2 startOrReload deploy/ecosystem.config.cjs --only shein-go-manager
```

The launcher reads `/home/ubuntu/shein-api-manager/.env`, maps its `DATABASE_URL`
to `SHEIN_DATABASE_URL`, and uses the existing `.web_session_secret`. No credential
values are copied into this repository.
