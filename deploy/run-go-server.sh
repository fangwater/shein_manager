#!/usr/bin/env bash
set -euo pipefail

cd /home/ubuntu/shein-api-manager
set -a
source .env
set +a

export SHEIN_DATABASE_URL="${SHEIN_DATABASE_URL:-${DATABASE_URL:-}}"
export SHEIN_WEB_SESSION_SECRET_FILE="${SHEIN_WEB_SESSION_SECRET_FILE:-/home/ubuntu/shein-api-manager/.web_session_secret}"

exec ./bin/shein-server
