#!/usr/bin/env bash
set -uo pipefail

cd /home/ubuntu/shein-api-manager
set -a
source .env
set +a

interval_seconds="${SHEIN_PRODUCT_SYNC_INTERVAL_SECONDS:-21600}"
while true; do
  if ! .venv/bin/python scripts/sync_latest_shein_data.py \
    --data products \
    --products-start-time "2020-01-01 00:00:00"; then
    echo "SHEIN product alias refresh failed" >&2
  fi
  sleep "${interval_seconds}"
done
