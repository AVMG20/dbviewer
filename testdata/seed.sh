#!/usr/bin/env bash
# Re-runnable seed for the dummy databases. Idempotent.
# Touches ONLY: MySQL dbviewer_dummy + dbviewer_dummy_ro, the dbviewer-pg container
# (its dbviewer_dummy database, port 55432) and testdata/dummy.sqlite + testdata/cache.sqlite.
#
# Usage: testdata/seed.sh [mysql,postgres,sqlite]
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="/opt/workbench/profile/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"

ONLY="${1:-mysql,postgres,sqlite}"

if [[ "$ONLY" == *postgres* ]]; then
  if ! docker inspect dbviewer-pg >/dev/null 2>&1; then
    docker run -d --name dbviewer-pg -e POSTGRES_PASSWORD=dbviewer -e POSTGRES_DB=dbviewer_dummy \
      -p 55432:5432 postgres:17-alpine >/dev/null
  elif [ "$(docker inspect -f '{{.State.Running}}' dbviewer-pg)" != "true" ]; then
    docker start dbviewer-pg >/dev/null
  fi
  for _ in $(seq 1 60); do
    docker exec dbviewer-pg pg_isready -U postgres -q && break
    sleep 1
  done
  # pg_isready can report ready during the init-time restart; wait for TCP queries too.
  for _ in $(seq 1 30); do
    docker exec dbviewer-pg psql -U postgres -h 127.0.0.1 -tAc 'select 1' >/dev/null 2>&1 && break
    sleep 1
  done
fi

go run ./testdata/seed -dir testdata -only "$ONLY"
