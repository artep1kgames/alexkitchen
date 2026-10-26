import json
import os
import sys
import tempfile
import urllib.parse
import urllib.request
from datetime import datetime

import openpyxl

BASE_DIR = "/home/lenasurudina/comments-app"
PUBLIC_URL = "https://disk.yandex.ru/i/NnH8A42jjvoy3g"

PRODUCTS_FILE = os.path.join(BASE_DIR, "products.json")
LOG_FILE = os.path.join(BASE_DIR, "products-sync.log")


def log(msg):
    line = f"{datetime.now().isoformat(timespec='seconds')} {msg}"
    print(line)
    with open(LOG_FILE, "a", encoding="utf-8") as f:
        f.write(line + "\n")


def get_download_url():
    api = (
        "https://cloud-api.yandex.net/v1/disk/public/resources/download"
        "?public_key=" + urllib.parse.quote(PUBLIC_URL, safe="")
    )

    with urllib.request.urlopen(api, timeout=30) as r:
        data = json.load(r)

    href = data.get("href")
    if not href:
        raise RuntimeError("Яндекс API не вернул href")

    return href


def download_xlsx():
    href = get_download_url()

    fd, path = tempfile.mkstemp(suffix=".xlsx")
    os.close(fd)

    try:
        urllib.request.urlretrieve(href, path)
        return path
    except Exception:
        try:
            os.remove(path)
        except OSError:
            pass
        raise


def bool_value(v):
    if isinstance(v, bool):
        return v

    if v is None:
        return True

    s = str(v).strip().lower()

    if s in ("true", "1", "да", "yes"):
        return True

    if s in ("false", "0", "нет", "no"):
        return False

    return True


def number(v, default=0):
    if v is None or v == "":
        return default

    if isinstance(v, (int, float)):
        return v

    s = str(v).strip().replace(",", ".")

    try:
        n = float(s)
        if n.is_integer():
            return int(n)
        return n
    except Exception:
        return default


def load_products(path):
    wb = openpyxl.load_workbook(path, data_only=False)

    if "Товары" not in wb.sheetnames:
        raise RuntimeError('В файле нет листа "Товары"')

    ws = wb["Товары"]

    headers = {}
    for col in range(1, ws.max_column + 1):
        value = ws.cell(row=1, column=col).value
        if value:
            headers[str(value).strip()] = col

    required = [
        "ID",
        "Раздел",
        "Подкатегория",
        "Товар",
        "Цена, ₽",
        "Цена за",
        "Остаток",
        "Ед. остатка",
        "Активен",
        "Порог «мало»",
        "Описание",
        "Картинка",
        "Сортировка",
    ]

    missing = [x for x in required if x not in headers]
    if missing:
        raise RuntimeError(
            "В таблице отсутствуют колонки: " + ", ".join(missing)
        )

    products = []

    for row in range(2, ws.max_row + 1):
        product_id = ws.cell(row, headers["ID"]).value

        if product_id is None or str(product_id).strip() == "":
            continue

        active = bool_value(
            ws.cell(row, headers["Активен"]).value
        )

        stock_raw = ws.cell(row, headers["Остаток"]).value
        threshold_raw = ws.cell(row, headers["Порог «мало»"]).value

        stock = None
        if stock_raw not in (None, ""):
            stock = number(stock_raw, 0)

        threshold = number(threshold_raw, 0)

        if not active:
            status = "Скрыт"
        elif stock is None:
            status = "В наличии"
        elif stock <= 0:
            status = "Нет в наличии"
        elif threshold > 0 and stock <= threshold:
            status = "Мало"
        else:
            status = "В наличии"

        product = {
            "id": str(product_id).strip(),
            "section": str(
                ws.cell(row, headers["Раздел"]).value or ""
            ).strip(),
            "category": str(
                ws.cell(row, headers["Подкатегория"]).value or ""
            ).strip(),
            "name": str(
                ws.cell(row, headers["Товар"]).value or ""
            ).strip(),
            "price": number(
                ws.cell(row, headers["Цена, ₽"]).value, 0
            ),
            "price_unit": str(
                ws.cell(row, headers["Цена за"]).value or ""
            ).strip(),
            "stock": stock,
            "stock_unit": str(
                ws.cell(row, headers["Ед. остатка"]).value or ""
            ).strip(),
            "active": active,
            "status": status,
            "low_stock_threshold": threshold,
            "description": str(
                ws.cell(row, headers["Описание"]).value or ""
            ).strip(),
            "image": str(
                ws.cell(row, headers["Картинка"]).value or ""
            ).strip(),
            "sort": number(
                ws.cell(row, headers["Сортировка"]).value, 0
            ),
        }

        products.append(product)

    if not products:
        raise RuntimeError(
            "В таблице не найдено ни одного товара"
        )

    products.sort(
        key=lambda x: (
            x.get("section", ""),
            x.get("category", ""),
            x.get("sort", 0),
            x.get("name", ""),
        )
    )

    return products


def save_products(products):
    tmp = PRODUCTS_FILE + ".tmp"

    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(
            products,
            f,
            ensure_ascii=False,
            indent=2,
        )

    if os.path.getsize(tmp) < 10:
        raise RuntimeError(
            "Новый products.json подозрительно маленький"
        )

    os.replace(tmp, PRODUCTS_FILE)


def main():
    xlsx = None

    try:
        log("START")

        xlsx = download_xlsx()
        products = load_products(xlsx)
        save_products(products)

        active = sum(1 for p in products if p["active"])
        sold_out = sum(
            1 for p in products
            if p["status"] == "Нет в наличии"
        )

        log(
            f"OK: товаров={len(products)} "
            f"активных={active} "
            f"нет_в_наличии={sold_out}"
        )

    except Exception as e:
        log(f"ERROR: {e}")
        sys.exit(1)

    finally:
        if xlsx and os.path.exists(xlsx):
            try:
                os.remove(xlsx)
            except OSError:
                pass


if __name__ == "__main__":
    main()
