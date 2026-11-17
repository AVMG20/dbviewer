# Test data

Dummy databases with the same Magento-like schema on MySQL, PostgreSQL and SQLite.
(Re)create everything with:

    testdata/seed.sh                 # all engines
    testdata/seed.sh mysql,sqlite    # a subset

The script is idempotent. It only ever drops/recreates the MySQL databases
`dbviewer_dummy` and `dbviewer_dummy_ro`, the `dbviewer_dummy` database inside the
`dbviewer-pg` container (started/created on demand), and `testdata/dummy.sqlite` +
`testdata/cache.sqlite`. It never touches the `polynux-postgres` container or port 5432.

## Connections

| Name | Driver | Details |
|---|---|---|
| dummy (local mysql) | MySQL 8.4 | socket `/opt/workbench/data/run/mysqld/mysql84.sock`, user `root`, empty password, database `dbviewer_dummy` |
| dummy_ro (production) | MySQL 8.4 | same socket/user, database `dbviewer_dummy_ro` (core tables only), use with **read-only + red** |
| dummy (postgres) | PostgreSQL 17 | `127.0.0.1:55432`, user `postgres`, password `dbviewer`, database `dbviewer_dummy`, sslmode `disable`; schemas `public` + `reporting` |
| dummy.sqlite | SQLite | `testdata/dummy.sqlite` |
| cache.sqlite | SQLite | `testdata/cache.sqlite` (kv_cache, http_cache, sessions) |

Postgres container (created by seed.sh if missing):

    docker run -d --name dbviewer-pg -e POSTGRES_PASSWORD=dbviewer -e POSTGRES_DB=dbviewer_dummy -p 55432:5432 postgres:17-alpine

Command line clients:

    /opt/workbench/profile/bin/mysql -uroot -S /opt/workbench/data/run/mysqld/mysql84.sock dbviewer_dummy
    docker exec -it dbviewer-pg psql -U postgres dbviewer_dummy
    sqlite3 testdata/dummy.sqlite

`sample.env` is a Laravel `.env` pointing at the MySQL dummy via `DB_SOCKET`
(for testing "Import from .env").

## Schema (all three engines)

Core tables (also in `dbviewer_dummy_ro`):

| Table | Rows | Notes |
|---|---|---|
| store | 4 | `store_id` PK, unique `code` |
| customer_entity | 600 | unicode names, nullable `dob`/`gender`, FK store |
| catalog_product_entity | 300 | unique `sku` |
| catalog_product_entity_varchar | ~770 | EAV values, nullable `value`, FKs to product + store |
| sales_order | 2000 | decimal(20,4) money, double `weight` (nullable), datetime, nullable `customer_id` (guests), `customer_note` text (NULL, `""`, multi-line, ~11 KB lorem (over the 8 KB cell limit), quotes + backslash), `extra_data` JSON (half NULL), `payment_payload` blob (1/6 set), FKs store + customer |
| sales_order_item | ~4900 | FK `order_id` → sales_order, nullable FK `product_id` |
| sales_order_status_history | ~3600 | FK `parent_id` → sales_order |
| sales_order_grid | 2000 | no FKs, `customer_email` |
| amasty_order_attribute_entity | ~1650 | `entity_id`, `parent_id`, `parent_entity_type` (1 = order, 2 = quote) |
| quote | 800 | |
| newsletter_subscriber | 300 | `subscriber_email` |
| core_config_data | ~45 | unique (scope, scope_id, path), nullable `value` |
| admin_user | 5 | |
| catalog_category_product | ~880 | **composite PK** (category_id, product_id) |
| customer_visitor_log | 400 | **no primary key**, contains duplicate rows |
| sales_order_summary_view | 2000 | **view** joining sales_order + store |

Plus 250 small filler tables (`amasty_*`, `mageplaza_*`, `catalog_*`, `sales_*`, ...;
0–15 rows each, PK `id`) so the sidebar looks like a real Magento database; many of
them have `customer_email`, `store_id`, `sku`, `status` columns, which makes column
speed search interesting. Total: 266 tables + views per database.

Postgres extras: schema `reporting` with `daily_sales` (date PK), `customer_ltv`
(FK to `public.customer_entity`), `export_log` (timestamptz, jsonb, text[], uuid,
identity PK), view `v_top_customers` and materialized view `mv_monthly_sales`.
Table/column comments on `sales_order`.

## App state for development

`testdata/appdata/` is the app-state dir used by browser dev mode
(`scripts/dev-browser.sh` sets `DBVIEWER_DATA_DIR` to it; secrets go to
`testdata/appdata/secrets.json`, as in the app). seed.sh
creates it with projects "Dummy shop" (4 connections) and "Other project"
(cache.sqlite) when it does not exist; delete the directory and re-run seed.sh to reset.
Go tests use a temporary data dir.
