#!/usr/bin/env bash
# quickshell — lightweight Go dev server for local-service-website-pack
# Usage: ./dev.sh [port]
set -euo pipefail

PORT="${1:-8080}"
ROOT="$(cd "$(dirname "$0")" && pwd)"

exec quickshell -d "$ROOT" -p "$PORT"
