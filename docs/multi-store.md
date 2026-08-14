# SHEIN multi-shop runtime

The SHEIN fulfillment console follows the same runtime model as Temu: one URL,
one Go process, and one PostgreSQL database. Operators switch shops from the
dashboard header and every API request selects a shop through `X-Shein-Shop`.

| Shop | Code | PostgreSQL schema | Public path |
| --- | --- | --- | --- |
| Beauty Hangers home | `beauty-hangers-home` | `shein_beauty_hangers_home` | `/shein/` |

`public.shein_shops` is the shared registry. It stores each shop's stable code,
display name, schema name, enabled state, and SHEIN credentials. The fulfillment
API returns only `code`, `name`, and `default`; it never returns credentials,
credential hints, API base URLs, or schema names.

Orders, returns, products, SKU mappings, inventory cost records, synchronization
state, fulfillment tasks, operation ledgers, quotes, batches, and label purchase
records live inside the registered shop schema. The tables keep `shop_key` as a
second isolation boundary even though each connection already uses
`search_path=<shop schema>,public`.

`public.shein_shops`, `health_checks`, and the `numbers_*` workbook tables remain
shared. XLWMS inventory and platform-order data remain owned by the warehouse
service and are queried through its API instead of being copied into a shop
schema.

The current production data was moved transactionally from `public` and the
legacy `default` key with:

```bash
go build -o bin/shein-shop-schema-migrate ./cmd/shop-schema-migrate
./bin/shein-shop-schema-migrate --dry-run
./bin/shein-shop-schema-migrate
```

The migration takes an advisory lock, enables cascading shop-key updates,
verifies every source and destination table row count, and moves tables with
`ALTER TABLE ... SET SCHEMA`. A dry run exercises the same transaction and then
rolls it back.

Register an additional shop through the existing authorization exchange, giving
it an explicit display name and schema:

```bash
python -m shein_api_manager exchange-token \
  --temp-token "TEMP_TOKEN_FROM_CALLBACK" \
  --save-shop second-shop \
  --shop-name "Second shop" \
  --schema-name shein_second_shop
```

For manually issued credentials, send the secret over standard input so it is
not stored in shell history:

```bash
read -rs SHEIN_NEW_SECRET
printf '%s' "$SHEIN_NEW_SECRET" | python -m shein_api_manager save-shop \
  --shop-key second-shop \
  --shop-name "Second shop" \
  --schema-name shein_second_shop \
  --open-key-id "OPEN_KEY_ID" \
  --secret-key-stdin
unset SHEIN_NEW_SECRET
```

Saving a shop creates its schema and applies both the Python data migrations and
the Go fulfillment migrations on normal service startup. The Python reporting
service resolves `SHEIN_SHOP_KEY` through the same registry and connects to that
shop schema.
