#!/usr/bin/env bash
set -euo pipefail

cd /home/ubuntu/shein-api-manager
set -a
source .env
set +a

export SHEIN_DATABASE_URL="${SHEIN_DATABASE_URL:-${DATABASE_URL:-}}"

exec ./bin/shein-server
