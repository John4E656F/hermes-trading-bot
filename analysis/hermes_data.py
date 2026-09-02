"""
Market data layer for the Hermes measurement scripts.

VENUE SUBSTITUTION -- read this before trusting any number downstream.

Hermes trades Bybit linear perpetuals. Bybit's API (api.bybit.com,
api.bytick.com, api.bybit.nl, testnet) is geo-blocked from this environment at
the CDN layer, as is Binance. OKX USDT-SWAP perpetuals are reachable and are
used instead. For the liquid pairs on the watchlist, OKX and Bybit OHLCV track
each other closely, but they are NOT identical: funding rates are set per
venue and differ materially, and thin alt-perp pairs can diverge more than
majors. Every result derived from this module is therefore an estimate of the
strategy's behaviour on OKX prices, not a replay of Bybit fills.

All responses are cached to ./cache so a re-run does not re-hit the API.
"""

import json
import os
import time
import urllib.request
import urllib.error

OKX = "https://www.okx.com"
CACHE = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "cache")
os.makedirs(CACHE, exist_ok=True)

BAR_MS = {"5m": 300_000, "15m": 900_000, "1H": 3_600_000, "4H": 14_400_000, "1D": 86_400_000}


def _get(url, retries=4):
    last = None
    for attempt in range(retries):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "hermes-research/1.0"})
            with urllib.request.urlopen(req, timeout=30) as r:
                return json.loads(r.read().decode())
        except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError, json.JSONDecodeError) as e:
            last = e
            time.sleep(2 ** attempt * 0.4)
    raise RuntimeError(f"GET failed after {retries} attempts: {url} ({last})")


def bybit_to_okx(symbol):
    """BTCUSDT -> BTC-USDT-SWAP"""
    if not symbol.endswith("USDT"):
        return None
    return f"{symbol[:-4]}-USDT-SWAP"


_inst_cache = None


def okx_instruments():
    global _inst_cache
    if _inst_cache is not None:
        return _inst_cache
    path = os.path.join(CACHE, "okx_instruments.json")
    if os.path.exists(path):
        _inst_cache = set(json.load(open(path)))
        return _inst_cache
    d = _get(f"{OKX}/api/v5/public/instruments?instType=SWAP")
    ids = [x["instId"] for x in d.get("data", []) if x["instId"].endswith("-USDT-SWAP")]
    json.dump(ids, open(path, "w"))
    _inst_cache = set(ids)
    return _inst_cache


def fetch_candles(symbol, bar="4H", start_ms=None, end_ms=None, max_bars=6000):
    """
    OHLCV for a Bybit-style symbol, ascending by time.

    Returns list of dicts: {ts, open, high, low, close, volume}
    Returns [] when the pair is not listed on OKX -- the caller must treat a
    missing pair as excluded from the sample, not as a pair with no signals.
    """
    inst = bybit_to_okx(symbol)
    if inst is None or inst not in okx_instruments():
        return []

    cache_path = os.path.join(CACHE, f"okx_{inst}_{bar}.json")
    rows = []
    if os.path.exists(cache_path):
        rows = json.load(open(cache_path))

    if not rows:
        # history-candles pages BACKWARD from `after` (exclusive upper bound).
        cursor = end_ms or int(time.time() * 1000)
        seen = set()
        while len(rows) < max_bars:
            url = f"{OKX}/api/v5/market/history-candles?instId={inst}&bar={bar}&limit=100&after={cursor}"
            d = _get(url)
            batch = d.get("data", [])
            if not batch:
                break
            for c in batch:
                ts = int(c[0])
                if ts in seen:
                    continue
                seen.add(ts)
                rows.append({"ts": ts, "open": float(c[1]), "high": float(c[2]),
                             "low": float(c[3]), "close": float(c[4]), "volume": float(c[5])})
            cursor = min(int(c[0]) for c in batch)
            if start_ms and cursor <= start_ms:
                break
            time.sleep(0.12)  # OKX public rate limit
        rows.sort(key=lambda r: r["ts"])
        json.dump(rows, open(cache_path, "w"))

    if start_ms:
        rows = [r for r in rows if r["ts"] >= start_ms]
    if end_ms:
        rows = [r for r in rows if r["ts"] <= end_ms]
    return rows


def fetch_funding_history(symbol, start_ms=None, max_rows=3000):
    """
    Historical funding rates, ascending. One entry per 8h settlement.
    Returns list of {ts, rate}. Empty when unavailable.
    """
    inst = bybit_to_okx(symbol)
    if inst is None or inst not in okx_instruments():
        return []

    cache_path = os.path.join(CACHE, f"okxfund_{inst}.json")
    if os.path.exists(cache_path):
        rows = json.load(open(cache_path))
    else:
        rows, cursor, seen = [], int(time.time() * 1000), set()
        while len(rows) < max_rows:
            url = f"{OKX}/api/v5/public/funding-rate-history?instId={inst}&limit=100&after={cursor}"
            try:
                d = _get(url)
            except RuntimeError:
                break
            batch = d.get("data", [])
            if not batch:
                break
            for c in batch:
                ts = int(c["fundingTime"])
                if ts in seen:
                    continue
                seen.add(ts)
                rows.append({"ts": ts, "rate": float(c["realizedRate"] or c["fundingRate"] or 0)})
            cursor = min(int(c["fundingTime"]) for c in batch)
            if start_ms and cursor <= start_ms:
                break
            time.sleep(0.12)
        rows.sort(key=lambda r: r["ts"])
        json.dump(rows, open(cache_path, "w"))

    if start_ms:
        rows = [r for r in rows if r["ts"] >= start_ms]
    return rows


def price_at(candles, ts_ms):
    """Close of the last candle at or before ts_ms. None if out of range."""
    lo, hi, found = 0, len(candles) - 1, None
    while lo <= hi:
        mid = (lo + hi) // 2
        if candles[mid]["ts"] <= ts_ms:
            found = candles[mid]
            lo = mid + 1
        else:
            hi = mid - 1
    return found["close"] if found else None


def fetch_oi_history(symbol, bar="4H", max_rows=6000):
    """
    Historical open interest, ascending: [{ts, oi}].

    S2 (OI/funding squeeze) needs a 24h OI change, so without this series S2
    is not replayable at all. Empty list means the series is unavailable for
    this pair -- the caller must exclude the pair from S2, not treat it as
    "no OI spike".
    """
    inst = bybit_to_okx(symbol)
    if inst is None or inst not in okx_instruments():
        return []

    cache_path = os.path.join(CACHE, f"okxoi_{inst}_{bar}.json")
    if os.path.exists(cache_path):
        return json.load(open(cache_path))

    rows, cursor, seen = [], int(time.time() * 1000), set()
    while len(rows) < max_rows:
        url = (f"{OKX}/api/v5/rubik/stat/contracts/open-interest-history"
               f"?instId={inst}&period={bar}&limit=100&end={cursor}")
        try:
            d = _get(url)
        except RuntimeError:
            break
        batch = d.get("data", [])
        if not batch:
            break
        new = 0
        for c in batch:
            ts = int(c[0])
            if ts in seen:
                continue
            seen.add(ts)
            rows.append({"ts": ts, "oi": float(c[1])})
            new += 1
        if new == 0:
            break
        cursor = min(int(c[0]) for c in batch)
        time.sleep(0.12)

    rows.sort(key=lambda r: r["ts"])
    json.dump(rows, open(cache_path, "w"))
    return rows
