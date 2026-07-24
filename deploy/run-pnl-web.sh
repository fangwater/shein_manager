#!/usr/bin/env bash
set -euo pipefail

deploy_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_root="$(cd "${deploy_dir}/.." && pwd)"

cd "${project_root}"
set -a
source .env
set +a

exec .venv/bin/python -m uvicorn shein_api_manager.pnl_web:app \
  --host 127.0.0.1 \
  --port 18992
