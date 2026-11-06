import argparse
import json
import os
from urllib.parse import urlparse

import psycopg

parser = argparse.ArgumentParser()
parser.add_argument("--apply", action="store_true")
parser.add_argument("--confirm-database")
parser.add_argument("--confirm-host")
args = parser.parse_args()

database_url = os.environ.get("DATABASE_URL")
if not database_url:
    raise SystemExit("ERROR: DATABASE_URL is not set")

parsed = urlparse(database_url)
host = parsed.hostname
database = parsed.path.lstrip("/")

if not host or not database:
    raise SystemExit("ERROR: Invalid DATABASE_URL")

if host.lower() in ("localhost", "127.0.0.1", "::1", "host.docker.internal"):
    raise SystemExit("ERROR: Local database connections are blocked")

with open("comments-app/products.json", encoding="utf-8") as f:
    products = json.load(f)

if not isinstance(products, list) or not products:
    raise SystemExit("ERROR: products.json must contain a non-empty array")

ids = [p.get("id") for p in products]
if any(not item for item in ids) or len(ids) != len(set(ids)):
    raise SystemExit("ERROR: Product IDs are missing or duplicated")

print(f"Database host: {host}")
print(f"Database name: {database}")
print(f"Products in file: {len(products)}")

if not args.apply:
    print("DRY RUN: no data was changed.")
    raise SystemExit(0)

if args.confirm_database != database:
    raise SystemExit("ERROR: Database confirmation does not match")

if args.confirm_host != host:
    raise SystemExit("ERROR: Host confirmation does not match")

sql = """
INSERT INTO products (
    id, section, category, name, price, price_unit,
    stock, stock_unit, active, status, low_stock_threshold,
    description, image, sort
)
VALUES (
    %(id)s, %(section)s, %(category)s, %(name)s, %(price)s, %(price_unit)s,
    %(stock)s, %(stock_unit)s, %(active)s, %(status)s, %(low_stock_threshold)s,
    %(description)s, %(image)s, %(sort)s
)
ON CONFLICT (id) DO UPDATE SET
    section = EXCLUDED.section,
    category = EXCLUDED.category,
    name = EXCLUDED.name,
    price = EXCLUDED.price,
    price_unit = EXCLUDED.price_unit,
    stock = EXCLUDED.stock,
    stock_unit = EXCLUDED.stock_unit,
    active = EXCLUDED.active,
    status = EXCLUDED.status,
    low_stock_threshold = EXCLUDED.low_stock_threshold,
    description = EXCLUDED.description,
    image = EXCLUDED.image,
    sort = EXCLUDED.sort
"""

with psycopg.connect(database_url) as conn:
    with conn.cursor() as cur:
        cur.executemany(sql, products)
        cur.execute("SELECT COUNT(*) FROM products")
        total = cur.fetchone()[0]

print(f"Import complete. Products in database: {total}")
