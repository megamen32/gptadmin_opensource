#!/usr/bin/env python3
import argparse
import csv
import html
import json
import re
import sys
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Any, Dict, List

BASE = 'https://ggsel.net'
DEFAULT_CATALOG = 'https://ggsel.net/catalog/openai-add-balance'
UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/119 Safari/537.36'


def fetch(url: str, timeout: int = 30) -> str:
    req = urllib.request.Request(url, headers={
        'User-Agent': UA,
        'Accept-Language': 'ru,en;q=0.9',
        'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8',
    })
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.read().decode('utf-8', 'replace')


def strip_html(s: str) -> str:
    s = re.sub(r'<script[^>]*>.*?</script>', ' ', s, flags=re.S|re.I)
    s = re.sub(r'<style[^>]*>.*?</style>', ' ', s, flags=re.S|re.I)
    s = re.sub(r'<[^>]+>', '\n', s)
    s = html.unescape(s)
    lines = [x.strip() for x in s.splitlines() if x.strip()]
    return '\n'.join(lines)


def extract_product_objects(page_html: str, section_id: int | None = 113785) -> List[Dict[str, Any]]:
    # Objects are JSON-escaped inside Next/React payload. The object ends with in_favorites.
    found: Dict[int, Dict[str, Any]] = {}
    pat = re.compile(r'\{"id_goods":\d+.*?"in_favorites":(?:true|false)\}')
    for m in pat.finditer(page_html):
        raw = m.group(0)
        try:
            obj = json.loads(raw)
        except Exception:
            continue
        gid = obj.get('id_goods')
        if not gid:
            continue
        if section_id is not None and obj.get('id_section') != section_id:
            # keep only category products, skip recommendations
            continue
        found[int(gid)] = obj
    return list(found.values())


def catalog_urls(base_catalog: str, max_pages: int) -> List[str]:
    urls = [base_catalog]
    # ggsel may use either ?page=N or no pagination. We probe gently; duplicate products stop later.
    for p in range(2, max_pages + 1):
        sep = '&' if '?' in base_catalog else '?'
        urls.append(f'{base_catalog}{sep}page={p}')
    return urls


def product_url(obj: Dict[str, Any]) -> str:
    return f"{BASE}/catalog/product/{obj['url']}"


def parse_detail_text(text: str) -> Dict[str, Any]:
    lines = [x.strip() for x in text.splitlines() if x.strip()]
    joined = '\n'.join(lines)
    desc = ''
    if 'О товаре' in lines:
        i = lines.index('О товаре')
        end = len(lines)
        for marker in ['Раскрыть', 'Все сделки на ggsel', 'Рекомендовано вам']:
            if marker in lines[i+1:]:
                end = min(end, i + 1 + lines[i+1:].index(marker))
        desc = '\n'.join(lines[i+1:end]).strip()
    seller = None
    # Usually seller appears near price line before buy/safety block; keep listing seller as primary if present.
    # This heuristic is only fallback.
    for idx, line in enumerate(lines):
        if re.fullmatch(r'\d+[\s\d]*\s*₽', line) and idx + 1 < len(lines):
            cand = lines[idx+1]
            if cand not in {'Купить', 'О товаре'}:
                seller = cand
                break
    reviews = None
    m = re.search(r'\((\d+)\)', joined)
    if m:
        reviews = int(m.group(1))
    return {
        'detail_text_excerpt': joined[:6000],
        'description': desc[:3000],
        'detail_seller_guess': seller,
        'detail_reviews_guess': reviews,
    }


def crawl_detail(obj: Dict[str, Any], budget: float) -> Dict[str, Any]:
    url = product_url(obj)
    out = {
        'id_goods': obj.get('id_goods'),
        'name': obj.get('name'),
        'seller_name': obj.get('seller_name'),
        'rating': obj.get('rating'),
        'cnt_sell': obj.get('cnt_sell'),
        'rate_rub_per_usd': float(obj.get('price_wmr_for_one') or 0),
        'price_usd_site': float(obj.get('price_wmz_for_one') or 0),
        'url': url,
        'error': '',
    }
    rate = out['rate_rub_per_usd']
    out['usd_for_budget'] = round(budget / rate, 6) if rate else 0
    try:
        page = fetch(url, timeout=30)
        text = strip_html(page)
        out.update(parse_detail_text(text))
    except Exception as e:
        out['error'] = repr(e)
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description='Crawl ggsel OpenAI balance catalog and product detail pages.')
    ap.add_argument('--catalog', default=DEFAULT_CATALOG)
    ap.add_argument('--budget', type=float, default=10000.0)
    ap.add_argument('--max-pages', type=int, default=5)
    ap.add_argument('--workers', type=int, default=8)
    ap.add_argument('--out-json', default='/tmp/ggsel-results/openai_balance_results.json')
    ap.add_argument('--out-csv', default='/tmp/ggsel-results/openai_balance_results.csv')
    args = ap.parse_args()

    products: Dict[int, Dict[str, Any]] = {}
    page_stats = []
    for url in catalog_urls(args.catalog, args.max_pages):
        try:
            page = fetch(url)
            objs = extract_product_objects(page)
            new_count = 0
            for o in objs:
                gid = int(o['id_goods'])
                if gid not in products:
                    products[gid] = o
                    new_count += 1
            page_stats.append({'url': url, 'found': len(objs), 'new': new_count})
            # If probing page=2 gives only duplicates/no goods, no need to continue.
            if url != args.catalog and new_count == 0:
                break
        except Exception as e:
            page_stats.append({'url': url, 'error': repr(e)})
            break

    rows = []
    with ThreadPoolExecutor(max_workers=args.workers) as ex:
        futs = [ex.submit(crawl_detail, obj, args.budget) for obj in products.values()]
        for fut in as_completed(futs):
            rows.append(fut.result())

    rows.sort(key=lambda r: (-r.get('usd_for_budget', 0), -(r.get('cnt_sell') or 0)))
    result = {
        'catalog': args.catalog,
        'budget_rub': args.budget,
        'crawled_at_epoch': time.time(),
        'page_stats': page_stats,
        'count': len(rows),
        'best': rows[0] if rows else None,
        'items': rows,
    }

    Path(args.out_json).parent.mkdir(parents=True, exist_ok=True)
    Path(args.out_csv).parent.mkdir(parents=True, exist_ok=True)
    Path(args.out_json).write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding='utf-8')

    fields = ['usd_for_budget','rate_rub_per_usd','name','seller_name','cnt_sell','rating','price_usd_site','url','description','error']
    with open(args.out_csv, 'w', newline='', encoding='utf-8') as f:
        w = csv.DictWriter(f, fieldnames=fields, extrasaction='ignore')
        w.writeheader()
        for r in rows:
            w.writerow(r)

    print(f"pages: {page_stats}")
    print(f"items: {len(rows)}")
    if rows:
        b = rows[0]
        print('BEST:')
        print(f"  {b['usd_for_budget']:.2f} USD for {args.budget:.0f} RUB")
        print(f"  rate: {b['rate_rub_per_usd']} RUB/USD")
        print(f"  seller: {b.get('seller_name')} sales={b.get('cnt_sell')} rating={b.get('rating')}")
        print(f"  name: {b.get('name')}")
        print(f"  url: {b.get('url')}")
    print(f"json: {args.out_json}")
    print(f"csv: {args.out_csv}")
    return 0

if __name__ == '__main__':
    raise SystemExit(main())
