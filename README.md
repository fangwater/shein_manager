# SHEIN API Manager

Local service code for SHEIN authorization, analytics, PostgreSQL synchronization, and production fulfillment.

Production uses the same database topology as Temu: `public.shein_shops` is a
shared registry and every enabled shop owns a private PostgreSQL schema. The
current shop is `beauty-hangers-home` (`Beauty Hangers home`) in
`shein_beauty_hangers_home`; see `docs/multi-store.md`.

The production fulfillment runtime is Go under `cmd/server`. It directly owns the nine consumer-order and integrated-logistics OpenAPI operations: order list/detail, address export, available warehouses and channels, online shipment creation and status checks, label printing, and tracking. Python remains responsible for authorization, historical/bulk synchronization, reporting, and the existing FastAPI management pages.

## Label Purchase Price Analysis

The Go fulfillment service stores an immutable price snapshot when a SHEIN
channel quote actually enters an online-order transaction. Channel previews
that are never submitted are retained only in the internal quote tables and do
not appear in the purchase analysis tables. Every row is isolated by
`shop_key`.

The relationship is:

```text
shein_go_shipping_quotes (1) -> (0..1) shein_label_purchase_choices
shein_label_purchase_choices (1) -> (1..3) shein_label_purchase_candidates
shein_label_purchase_choices (many) -> (1) shein_go_api_operations
```

The choice-to-operation join uses `shop_key`,
`operation_idempotency_key = idempotency_key`, and
`operation = 'place-express-order'`. Successful SHEIN results are also linked
directly through `place_request_id` and `delivery_no`.

### `shein_label_purchase_choices`

This is the analysis header. It contains one row per `preRequestId` that
entered a `place-express-order` transaction and always stores the final
operator-selected channel, including when it is outside the three lowest
prices.

| Column | Stored data |
| --- | --- |
| `shop_key` | Shop isolation key; part of the primary key. |
| `pre_request_id` | SHEIN quote ID and part of the primary key. |
| `operation_idempotency_key` | Joins the snapshot to the protected Go operation ledger. |
| `order_no` | SHEIN consumer order number. |
| `selection_source` | `manual` for an operator-selected purchase or `automatic` for the automatic fulfillment worker. |
| `selected_price_rank` | `1`, `2`, or `3` when selected in the low-price Top 3; `NULL` when outside it. |
| `selected_warehouse_address_code` | SHEIN warehouse address code used to obtain the quote. |
| `selected_express_id` | SHEIN logistics-company ID. |
| `selected_express_id_code` | SHEIN logistics-company code/name. |
| `selected_express_channel_code` | Channel code sent to online ordering. |
| `selected_express_short_name` | Channel short name returned by SHEIN. |
| `selected_performance_cost` | Selected live fulfillment fee as `numeric(18,4)`. |
| `selected_currency_code` | Currency returned by SHEIN; empty means SHEIN omitted it. |
| `selected_estimate_min_day` | Fastest estimated delivery days, when returned. |
| `selected_estimate_max_day` | Slowest estimated delivery days, when returned. |
| `selection_reason` | `operator_selected` for manual purchases or `lowest_available_price` for automatic purchases. |
| `place_request_id` | Async ordering request ID, populated after a successful API call. |
| `delivery_no` | SHEIN delivery number, populated after a successful API call. |
| `purchased_at` | Time the purchase transaction reserved the snapshot. |

### `shein_label_purchase_candidates`

This is the Top 3 detail table. It contains one to three rows for each
`shop_key + pre_request_id`, ranked by comparable `performanceCost`.

| Column | Stored data |
| --- | --- |
| `shop_key` | Shop isolation key and part of the primary key. |
| `pre_request_id` | Foreign key to the purchase choice and part of the primary key. |
| `price_rank` | Low-price rank `1` through `3`; part of the primary key. |
| `warehouse_address_code` | Warehouse selected before this SHEIN quote was requested. |
| `express_id` | SHEIN logistics-company ID. |
| `express_id_code` | SHEIN logistics-company code/name. |
| `express_channel_code` | SHEIN channel code. |
| `express_short_name` | SHEIN channel short name. |
| `performance_cost` | Candidate live fulfillment fee as `numeric(18,4)`. |
| `currency_code` | Candidate currency. |
| `estimate_min_day` | Fastest estimated delivery days, when returned. |
| `estimate_max_day` | Slowest estimated delivery days, when returned. |
| `is_selected` | `true` only when this ranked candidate is the final selection. All rows are `false` when selection is outside the Top 3. |

### Snapshot Rules

1. A successful `order-mapping-channels` response is normalized into internal
   quote tables keyed by `shop_key + preRequestId`. Customer data and raw API
   responses are not stored in this Go snapshot.
2. SHEIN requires `warehouseAddressCode` before returning channel prices. The
   automatic worker first queries live OMS inventory for the mapped warehouse
   SKU and only quotes operated DPS/ARP warehouses that can cover the complete
   order and pass the configured safety-stock rules. An incomplete inventory
   response or a manual-review decision blocks automatic label purchase. PG,
   out-of-stock, and other unavailable warehouses are not quoted. A manual
   purchase keeps the candidates within the operator-selected quote and
   warehouse.
3. Candidates are comparable only when they use the selected channel's
   currency and include `performanceCost`. Ranking is by fee, then ARP over
   DPS on a tie, then `expressChannelCode`. This ranking only chooses which
   label to buy. After purchase, outbound follows the bought warehouse.
4. The automatic worker first drops UNIUNI, non-whitelist, and shop-disabled
   carriers for the quoted OMS warehouse. Among remaining quotes it selects the
   lowest live price; same-price ties prefer ARP over DPS. ARP East defaults
   UNIUNI and SwiftX to disabled. Manual purchases preserve the operator's
   choice only when that channel is still allowed. The purchase header is
   written before the SHEIN online-order call; candidates and the header are
   committed atomically. A later OMS or parcel step cannot change that
   warehouse; a mismatch is manual.
5. A new Go quote must have a valid snapshot or ordering is blocked. Historical
   `preRequestId` values created before this feature remain orderable but do
   not create fabricated analysis rows. Historical data is not backfilled.

Example price-premium query:

```sql
SELECT
    choice.shop_key,
    choice.order_no,
    choice.purchased_at,
    choice.selected_express_channel_code,
    choice.selected_price_rank,
    choice.selected_performance_cost,
    min(candidate.performance_cost) AS lowest_eligible_cost,
    choice.selected_performance_cost - min(candidate.performance_cost)
        AS selected_price_premium
FROM shein_label_purchase_choices choice
JOIN shein_label_purchase_candidates candidate
  USING (shop_key, pre_request_id)
GROUP BY
    choice.shop_key,
    choice.pre_request_id,
    choice.order_no,
    choice.purchased_at,
    choice.selected_express_channel_code,
    choice.selected_price_rank,
    choice.selected_performance_cost
ORDER BY choice.purchased_at DESC;
```

## Fulfillment Queues

The Go console classifies live store orders into the same operational queues
used by the Temu service. Platform-logistics orders buy a SHEIN label the same
way Temu buys a label: query warehouses, quote channels, then place the
express order. Automatic fulfillment does not call `export-address` first.
An order is eligible for automatic fulfillment only when it has exactly one
goods line and quantity 1, uses integrated logistics, is not an
authentication-warehouse order, is in a processable label state, has one
exact `skuCode` mapping, and that mapping has a complete enabled warehouse
package specification. Multiple SKUs or a single SKU with quantity greater
than 1 stay in the manual queue, the same way Temu treats `multi_item`.
Orders that do not meet every condition remain visible in the manual queue
with a concrete reason. Merchant self-ship orders stay on the address-export
path. SHEIN OpenAPI in this service has no Temu-style combined-shipment
candidate endpoint (`bg.order.combinedshipment.list.get`); there is no
console “可合并订单” view.

Automatic fulfillment jobs are persisted in
`shein_go_auto_fulfillment_jobs`. Each job records its current step, attempt,
selected warehouse and channel, price, result identifiers, and any sanitized
error. Failed jobs move to the exception queue and can be retried without
losing their operation-ledger history.

One-click fulfillment creates a persistent batch in
`shein_go_bulk_fulfillment_batches` with ordered items in
`shein_go_bulk_fulfillment_items`. A batch runs one order at a time and stops
at the first failure. Restarting continues from the failed item; already
completed orders are never submitted again.


## Setup

```bash
cd /home/ubuntu/shein-api-manager
python3 -m venv .venv
. .venv/bin/activate
pip install -r requirements.txt
pip install -e .
cp .env.example .env
```

Fill `.env` with your local PostgreSQL `DATABASE_URL` and SHEIN app credentials.

Initialize the database:

```bash
python -m shein_api_manager init-db
```

## Authorization Flow

Generate an authorization URL for the merchant to open with the store main account:

```bash
python -m shein_api_manager auth-url \
  --redirect-url "https://your-domain.example/shein/callback" \
  --state "beauty-hangers-home"
```

After authorization, SHEIN redirects to your `redirectUrl` with `tempToken` and `state`.
Exchange that token for store credentials:

```bash
python -m shein_api_manager exchange-token \
  --temp-token "TEMP_TOKEN_FROM_CALLBACK" \
  --save-shop beauty-hangers-home \
  --shop-name "Beauty Hangers home" \
  --schema-name shein_beauty_hangers_home
```

This stores the store-level `openKeyId` and decrypted `secretKey` in PostgreSQL.
Treat the database as sensitive.

## Manual Store Credentials

If you already have store credentials:

```bash
read -rs SHEIN_NEW_SECRET
printf '%s' "$SHEIN_NEW_SECRET" | python -m shein_api_manager save-shop \
  --shop-key beauty-hangers-home \
  --shop-name "Beauty Hangers home" \
  --schema-name shein_beauty_hangers_home \
  --open-key-id "..." \
  --secret-key-stdin
unset SHEIN_NEW_SECRET
```

## API Wrappers

Call order list:

```bash
python -m shein_api_manager order-list \
  --shop-key beauty-hangers-home \
  --params-json '{"queryType":1,"startTime":"2026-06-26 00:00:00","endTime":"2026-06-27 23:59:59","page":1,"pageSize":30}'
```

Call order detail:

```bash
python -m shein_api_manager order-detail \
  --shop-key beauty-hangers-home \
  --order-no "ORDER_NO_1" \
  --order-no "ORDER_NO_2"
```

## Download Orders

SHEIN currently requires `queryType`, `startTime`, `endTime`, `page`, and
`pageSize`. `startTime` and `endTime` must be no more than 48 hours apart.

```bash
python -m shein_api_manager download-orders \
  --shop-key beauty-hangers-home \
  --params-json '{"queryType":1,"startTime":"2026-06-26 00:00:00","endTime":"2026-06-27 23:59:59"}' \
  --page-param page \
  --page-size-param pageSize \
  --page-size 30 \
  --fetch-details
```

The local tables store raw JSON payloads so the integration remains resilient while we
confirm the exact SHEIN response shape.

## Product Sync

Use the lightweight published-product endpoint to collect SPU/SKC/SKU platform codes:

```bash
python -m shein_api_manager sync-products \
  --shop-key beauty-hangers-home \
  --params-json '{"insertTimeStart":"2026-01-01 00:00:00","insertTimeEnd":"2026-02-01 00:00:00"}' \
  --page-size 500
```

Use the comprehensive product search endpoint for SPU-level product details, including
SKC/SKU codes, supplier SKUs, titles, main images, shelf status, prices, and inventory.
SHEIN limits this API to `pageSize <= 10`:

```bash
python -m shein_api_manager sync-product-details \
  --shop-key beauty-hangers-home \
  --params-json '{"createTimeStart":"2026-01-01 00:00:00","createTimeEnd":"2026-02-01 00:00:00","languageList":["zh-cn","en"]}' \
  --page-size 10
```

Raw wrappers are also available as `product-list` for
`/open-api/openapi-business-backend/product/query` and `product-search` for
`/open-api/goods/searchProduct`. Product sync stores raw JSON in PostgreSQL and
feeds the SKU mapping page, so products that have not yet appeared in orders can
be mapped after `sync-products` or `sync-product-details` runs.

## Order Status And Returns

Order sync stores SHEIN's raw `order_status` plus Chinese `order_status_label`.
`orderStatus=6` is stored as `用户已退款`; it is not treated as a return.
Order sync also stores raw `order_type` and Chinese `order_type_label` based on
SHEIN's `orderType` enum.

Returns must be queried from SHEIN's official return APIs separately:

- `/open-api/return-order/list` pages return orders into `shein_order_returns`.
  The observed API limit is `pageSize` 1-30 and a maximum 48-hour time window.
- `/open-api/return-order/details` enriches those rows with full return-order
  details. Details are requested in batches of up to 30 `returnOrderNo` values.

The sync is sequential by default and rate-limited to 1 request/second for both
list and detail calls:

```bash
python -m shein_api_manager sync-order-returns \
  --shop-key beauty-hangers-home \
  --params-json '{"startTime":"2026-06-26 00:00:00","endTime":"2026-06-27 23:59:59","queryType":1}'
```

To clean legacy inferred return flags and backfill official order status/type
labels from stored order payloads:

```bash
python -m shein_api_manager backfill-order-returns --shop-key beauty-hangers-home
```

The command name is kept for compatibility; it no longer infers returns from
order JSON.

## Resume-Safe Full Order Sync

Use `sync-orders-full` to pull order lists and full order details across a larger
time range. The command automatically splits the range into SHEIN-compatible
windows of up to 48 hours, paginates each window with `page`/`pageSize`, and
fetches details in batches of up to 30 order numbers. Progress is stored in
PostgreSQL after each list page and detail batch, so rerunning the same
`--sync-key` resumes unfinished windows.

```bash
python -m shein_api_manager sync-orders-full \
  --shop-key beauty-hangers-home \
  --sync-key default-202606-orders \
  --start-time "2026-06-01 00:00:00" \
  --end-time "2026-06-28 23:59:59" \
  --query-type 1 \
  --page-size 30 \
  --list-rps 20 \
  --detail-rps 50
```

Useful options:

- `--query-type 1` queries by order allocation time; `--query-type 2` queries by update time.
- `--params-json` can pass optional filters like `orderStatus`, `queryOrderType`, `cteInvoiceStatus`, or `nfeInvoiceStatus`.
- `--sync-key` is the resume key. Use the same key to continue after interruption.
- `--reset` clears saved progress for that key and starts over.
- `--list-rps` must be at most 100; `--detail-rps` must be at most 300. Defaults are conservative.

## Manual Latest Sync

Use the standalone helper when you want to manually catch up from the current
PostgreSQL progress for orders, official return orders, and product details:

```bash
cd /home/ubuntu/shein-api-manager
.venv/bin/python scripts/sync_latest_shein_data.py
```

Preview the inferred ranges without calling SHEIN:

```bash
.venv/bin/python scripts/sync_latest_shein_data.py --dry-run
```

Sync only one data type:

```bash
.venv/bin/python scripts/sync_latest_shein_data.py --data orders
.venv/bin/python scripts/sync_latest_shein_data.py --data returns
.venv/bin/python scripts/sync_latest_shein_data.py --data products
```

The helper uses `order_updated_at` for order progress and `last_update_time` for
return progress, with a default 60-minute overlap. Product APIs are queried by
create/insert time; the existing product tables only store `last_seen_at`, so the
helper uses that as the default product cursor unless `--products-start-time` is
provided.

## Notes

- Semi-managed apps use `https://openapi.sheincorp.com` for production API calls.
- Authorization uses a different host: `https://openapi-sem.sheincorp.com`.
- App credentials (`APP_ID`/`APP_Secretkey`) are for `/open-api/auth/get-by-token`.
- Order APIs use store credentials: `openKeyId` and decrypted `secretKey`.

## Go Fulfillment Server

Build and test the production fulfillment service:

```bash
go test ./...
mkdir -p bin
go build -o bin/shein-server ./cmd/server
pm2 startOrReload deploy/ecosystem.config.cjs --only shein-go-manager
pm2 save
```

The Go service listens on `127.0.0.1:18084` and is routed under `/shein/`. It reads `DATABASE_URL` from the same private `.env` file and reuses the existing `shein_shops` credentials and `shein_pnl_session` login cookie. See `docs/go-fulfillment.md` for the local API and safety contract.

## Python Web Deployment

All PNL, logistics, shipping-fee, returns, SKU mapping, warehouse relation, and
inventory pages are served by one FastAPI application. Production runs one PM2
process on `127.0.0.1:18992`; Nginx is the only public entry point.

Start or update the service and persist the PM2 process list:

```bash
pm2 startOrReload deploy/ecosystem.config.cjs --only shein-warehouse-relations-public
pm2 save
```

The launcher reads the project `.env` before starting Uvicorn. Do not add secrets
to the PM2 ecosystem file or expose the Uvicorn port directly to the internet.
