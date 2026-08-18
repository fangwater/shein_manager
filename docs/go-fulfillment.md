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
channel quote, and online label purchase. A successful purchase closes the
dialog, leaves the pending queue, and opens the shared 自动处理中 view instead
of a raw OpenAPI drawer. Only operated DPS/ARP warehouses remain selectable
for platform labels; PG and other platform-listed warehouses stay visible as
unavailable and cannot be quoted. `export-address` with `handleType=2` is only
used for merchant self-ship orders that cannot buy a platform label.

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
| `GET /api/oms-platform-orders/accounts` | List XLWMS accounts and their live Lingxing login status |
| `GET /api/oms-platform-orders` | List labeled Lingxing parcels and their warehouse / OMS status |
| `POST /api/oms-platform-orders/sync` | Recheck warehouse and OMS status for labeled parcels |
| `GET /api/oms-platform-orders/{orderNo}?account=all` | Query one order across enabled accounts |
| `POST /api/orders/{orderNo}/warehouse-preview` | Query live inventory and package specs for the order warehouse SKUs |
| `GET /api/orders/{orderNo}/xlwms-parcel` | Prefill a Lingxing parcel-create draft after a DPS label is ready |
| `POST /api/orders/{orderNo}/xlwms-parcel` | Create the Lingxing parcel and upload the SHEIN label |
| `POST /api/orders/{orderNo}/xlwms-parcel-label` | Re-upload the SHEIN label to an existing Lingxing parcel |

The order query returns only fulfillment verification fields. Raw XLWMS order
objects and customer details are not passed through.

Beauty Hangers Home (`beauty-hangers-home`) is hardcoded to require a manual
Lingxing parcel after a DPS label is generated. Other shops do not inherit this
rule. The processing page prefill uses the latest fulfillment task, the stored
order snapshot, live `export-address` with `handleType=1`, and queue SKU
mappings. SHEIN express channel codes are not Lingxing warehouse channels; the
form defaults `logisticsChannel` to `Upload_Shipping_Label`. Create downloads
the current SHEIN `filePdfUrl` and attaches it as Lingxing `label.fileData` /
`fileList` `bizCode=1`. Creating again first cancels the latest active
Lingxing parcel, then waits for `selectBizStatus` or detail `status=4` so a
truncated receiver or wrong store can be rebuilt on a new outbound number.
Lookup prefers `thirdOrderNoList` and falls back to `referOrderNoList`, because
canceled recreates often leave the GSU number only on `referOrderNo`.
The processing view has a fifth “待补传”
status card for DPS orders that already have a SHEIN label but still need a
Lingxing outbound with that label. The task list attaches live Lingxing
parcel status; once an active outbound has the label, the order leaves the
待补传 card, the 面单已就绪 card, and the default processing total, then
enters 自动发货账本 and 领星订单状态. A background watcher queries the
bought-label OMS account and the opposite account, records warehouse status
0/1/2/3, and syncs the same snapshot into XLWMS `fulfillment-audits`.
If SHEIN has already shipped or delivered the order and there is no active
Lingxing outbound, the watcher archives the row itself instead of waiting for
a missing platform order or marking it as a leak. A second “补传面单” button calls SHEIN
`print-express-info` for a fresh `filePdfUrl` and uploads it through XLWMS
`POST /outbound/tracking-label-update` (`updateTrackNoAndLabel`). Custom-channel
warehouse-processing and label-exception parcels can use that button; draft,
canceled, and shipped parcels stay disabled because Lingxing rejects those
updates. Receiver names join
`firstName` + `lastName` from
`export-address`. The
SHEIN order number is sent as `platformOrderNo` with `salesPlatform=SHEIN` and
the Lingxing authorized store `BeautyHanger` as both official `store` and
local `storeName`. Lingxing still requires `thirdOrderNo` on
create, so the same GSU number is copied there; `referOrderNo` stays empty.
Address responses stay `Cache-Control: no-store` and are not cached.

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
