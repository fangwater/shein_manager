# SHEIN Go Manager

The service runs as a standalone Go process on `127.0.0.1:18084` and is exposed
through `https://pangutech.online/shein/`. It reads enabled shops from the
shared `public.shein_shops` registry and creates one handler, database pool, and
automatic-fulfillment worker set per shop. Each pool uses the shop's private
PostgreSQL schema followed by `public` in its `search_path`.

## Local API

Use `X-Shein-Shop` to select the shop for every local API request. Missing
headers use `beauty-hangers-home`. A legacy `shop_key` field may be sent in a
POST envelope only when it matches the routed header. `GET /api/system/shops`
uses the same `{code,name,default}` contract as Temu. All POST endpoints accept
this envelope:

```json
{
  "shop_key": "beauty-hangers-home",
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

The page at `/shein/` follows the same shop-level operating model as the Temu
console and does not require a web login. Operators select a shop, query orders,
and for SHEIN platform-logistics orders go straight to warehouse selection,
channel quote, and online label purchase. Only operated DPS/ARP warehouses
remain selectable for platform labels; PG and other platform-listed warehouses
stay visible as unavailable and cannot be quoted. `export-address` with
`handleType=2` is only used for merchant self-ship orders that cannot buy a
platform label.

The selected shop is sent in `X-Shein-Shop` and kept in session storage. The
shop selector displays `Beauty Hangers home` while requests use the stable
`beauty-hangers-home` code. The
browser stores only normalized task references per shop: order number, channel
code, `placeRequestId`, `deliveryNo`, status, and update time. Customer
addresses and raw OpenAPI responses are not persisted in browser storage.

## Shared Console and XLWMS

The SHEIN console loads the canonical Temu fulfillment shell from
`/temu/dashboard.css` and keeps only SHEIN-specific controls in
`platform.css`. The navigation order, table density, status components,
responsive shell, XLWMS inventory matrix, and package-spec presentation are
therefore shared across storefronts.

The Go service connects to the warehouse manager through `XLWMS_BASE_URL`,
defaulting to `https://pangutech.online/warehouse-console/api`. It exposes
these public, no-store fulfillment queries:

| Local endpoint | Purpose |
| --- | --- |
| `GET /api/oms-platform-orders/accounts` | List enabled XLWMS accounts |
| `GET /api/oms-platform-orders/{orderNo}?account=all` | Query the order across enabled accounts |
| `POST /api/orders/{orderNo}/warehouse-preview` | Query live inventory and package specs for the order warehouse SKUs |

The order query returns only fulfillment verification fields. Raw XLWMS order
objects and customer details are not passed through.

## Safety

`place-express-order`, `print-express-info`, and `export-address` with
`handleType=2` require an exact `X-Confirm-Shein-Action` header plus an
`Idempotency-Key`. Reusing the same key and request returns the stored result;
reusing it for different data is rejected.

The fulfillment page and its APIs do not require a `shein_pnl_session` cookie.
Address responses and all API responses use `Cache-Control: no-store`. Logs include
only the operation, shop, duration, error code, and trace ID.
They do not include request bodies, credentials, addresses, or order numbers.

## Runtime

```bash
mkdir -p bin
go build -o bin/shein-server ./cmd/server
pm2 startOrReload deploy/ecosystem.config.cjs --only shein-go-manager
```

The launcher reads `/home/ubuntu/shein-api-manager/.env` and maps its `DATABASE_URL`
to `SHEIN_DATABASE_URL`. No credential values are copied into this repository.
The multi-shop database topology and registration procedure are documented in
`docs/multi-store.md`.
