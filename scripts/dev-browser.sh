#!/usr/bin/env bash
# Browser dev mode: run the UI in any browser with live Go bindings.
#   - Go backend in Wails *server mode* (no window) on 127.0.0.1:$WAILS_SERVER_PORT (9246)
#   - Vite dev server with HMR on http://localhost:$WAILS_VITE_PORT (9245),
#     proxying /wails/* (bindings + events websocket) to the backend.
# Open http://localhost:9245 . Ctrl+C stops both. Go changes need a restart.
#
# Env: DBVIEWER_DATA_DIR (default: testdata/appdata, so dev never touches the real app state),
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="/opt/workbench/profile/bin:$HOME/go/bin:$PATH"
export WAILS_VITE_PORT="${WAILS_VITE_PORT:-9245}"
export WAILS_SERVER_PORT="${WAILS_SERVER_PORT:-9246}"
export DBVIEWER_DATA_DIR="${DBVIEWER_DATA_DIR:-$PWD/testdata/appdata}"
if [ "$(uname)" = Darwin ]; then
  export CGO_CFLAGS="-mmacosx-version-min=12.0" CGO_LDFLAGS="-mmacosx-version-min=12.0" MACOSX_DEPLOYMENT_TARGET=12.0
fi

wails3 generate bindings -clean=true -ts -i >/dev/null
mkdir -p frontend/dist bin
[ -e frontend/dist/index.html ] || echo '<!doctype html><title>dev</title>' > frontend/dist/index.html
go build -tags server -o bin/dbviewer-devserver .

pids=()
cleanup() { kill "${pids[@]}" 2>/dev/null || true; }
trap cleanup EXIT INT TERM

(cd frontend && exec npx vite --clearScreen false) & pids+=($!)
for _ in $(seq 1 60); do
  curl -fs -o /dev/null "http://127.0.0.1:$WAILS_VITE_PORT/" && break
  sleep 0.5
done
FRONTEND_DEVSERVER_URL="http://127.0.0.1:$WAILS_VITE_PORT" ./bin/dbviewer-devserver & pids+=($!)
echo "dbviewer browser dev: http://localhost:$WAILS_VITE_PORT  (backend :$WAILS_SERVER_PORT, data $DBVIEWER_DATA_DIR)"
wait
