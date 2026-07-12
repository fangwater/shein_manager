# SHEIN API Manager

Local service code for SHEIN authorization, order API wrappers, and PostgreSQL order downloads.

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
  --state "default"
```

After authorization, SHEIN redirects to your `redirectUrl` with `tempToken` and `state`.
Exchange that token for store credentials:

```bash
python -m shein_api_manager exchange-token \
  --temp-token "TEMP_TOKEN_FROM_CALLBACK" \
  --save-shop default
```

This stores the store-level `openKeyId` and decrypted `secretKey` in PostgreSQL.
Treat the database as sensitive.

## Manual Store Credentials

If you already have store credentials:

```bash
python -m shein_api_manager save-shop \
  --shop-key default \
  --open-key-id "..." \
  --secret-key "..."
```

## API Wrappers

Call order list:

```bash
python -m shein_api_manager order-list \
  --shop-key default \
  --params-json '{"queryType":1,"startTime":"2026-06-26 00:00:00","endTime":"2026-06-27 23:59:59","page":1,"pageSize":30}'
```

Call order detail:

```bash
python -m shein_api_manager order-detail \
  --shop-key default \
  --order-no "ORDER_NO_1" \
  --order-no "ORDER_NO_2"
```

## Download Orders

SHEIN currently requires `queryType`, `startTime`, `endTime`, `page`, and
`pageSize`. `startTime` and `endTime` must be no more than 48 hours apart.

```bash
python -m shein_api_manager download-orders \
  --shop-key default \
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
  --shop-key default \
  --params-json '{"insertTimeStart":"2026-01-01 00:00:00","insertTimeEnd":"2026-02-01 00:00:00"}' \
  --page-size 500
```

Use the comprehensive product search endpoint for SPU-level product details, including
SKC/SKU codes, supplier SKUs, titles, main images, shelf status, prices, and inventory.
SHEIN limits this API to `pageSize <= 10`:

```bash
python -m shein_api_manager sync-product-details \
  --shop-key default \
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
  --shop-key default \
  --params-json '{"startTime":"2026-06-26 00:00:00","endTime":"2026-06-27 23:59:59","queryType":1}'
```

To clean legacy inferred return flags and backfill official order status/type
labels from stored order payloads:

```bash
python -m shein_api_manager backfill-order-returns --shop-key default
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
  --shop-key default \
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
