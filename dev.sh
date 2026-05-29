#!/usr/bin/env bash
# qserve — lightweight Go dev server for local-service-website-pack
# Usage: ./dev.sh [port]
set -euo pipefail

PORT="${1:-8080}"
ROOT="$(cd "$(dirname "$0")" && pwd)"

exec qserve -d "$ROOT" -p "$PORT"
