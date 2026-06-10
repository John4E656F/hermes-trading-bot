#!/usr/bin/env python3
"""
Analyze kronos_log.jsonl: for every Kronos AI prediction logged by the bot,
fetch the current live price and compare it to the price at prediction time
to see whether Kronos or the bot's own indicator-based master signal called
the move correctly.

Usage:
    python3 analyze_kronos.py
"""
import json
import time
import requests

LOG_PATH = "kronos_log.jsonl"


def fetch_price(symbol):
    resp = requests.get(
        f"https://api.bybit.com/v5/market/tickers?category=linear&symbol={symbol}",
        timeout=10,
    )
    data = resp.json()
    if data.get("retCode") != 0:
        return None
    return float(data["result"]["list"][0]["lastPrice"])


def direction_correct(direction, change_pct):
    direction = direction.lower()
    if direction in ("buy", "long"):
        return change_pct > 0
    if direction in ("sell", "short"):
        return change_pct < 0
    return None  # hold/neutral - no call made


def main():
    try:
        with open(LOG_PATH) as f:
            lines = [json.loads(l) for l in f if l.strip()]
    except FileNotFoundError:
        print(f"{LOG_PATH} not found — no Kronos predictions logged yet.")
        return

    if not lines:
        print("No entries yet.")
        return

    price_cache = {}
    stats = {
        "agree": {"kronos_right": 0, "kronos_wrong": 0, "no_call": 0},
        "disagree": {"kronos_right": 0, "master_right": 0, "both_wrong": 0, "both_right": 0, "no_call": 0},
        "neutral": {"kronos_right": 0, "kronos_wrong": 0, "no_call": 0},
    }

    print(f"{'Symbol':<14} {'Agreement':<10} {'Master':<5} {'Kronos':<6} {'PredPrice':>12} {'NowPrice':>12} {'Chg%':>7}")
    print("-" * 75)

    for entry in lines:
        symbol = entry["symbol"]
        if symbol not in price_cache:
            price_cache[symbol] = fetch_price(symbol)
            time.sleep(0.1)  # be polite to the API
        now_price = price_cache[symbol]
        pred_price = entry.get("price") or entry.get("kronos_price")
        if not now_price or not pred_price:
            continue

        change_pct = (now_price - pred_price) / pred_price * 100.0
        agreement = entry.get("agreement", "neutral")
        master = entry.get("master_action", "HOLD")
        kronos_dir = entry.get("kronos_direction", "hold")

        kronos_ok = direction_correct(kronos_dir, change_pct)
        master_ok = direction_correct(master, change_pct)

        bucket = stats.setdefault(agreement, {"kronos_right": 0, "kronos_wrong": 0, "no_call": 0,
                                                "master_right": 0, "both_wrong": 0, "both_right": 0})

        if agreement == "disagree":
            if kronos_ok is None and master_ok is None:
                bucket["no_call"] += 1
            elif kronos_ok and not master_ok:
                bucket["kronos_right"] += 1
            elif master_ok and not kronos_ok:
                bucket["master_right"] += 1
            elif kronos_ok and master_ok:
                bucket["both_right"] += 1
            else:
                bucket["both_wrong"] += 1
        else:
            if kronos_ok is None:
                bucket["no_call"] += 1
            elif kronos_ok:
                bucket["kronos_right"] += 1
            else:
                bucket["kronos_wrong"] += 1

        print(f"{symbol:<14} {agreement:<10} {master:<5} {kronos_dir:<6} "
              f"{pred_price:>12.4f} {now_price:>12.4f} {change_pct:>6.2f}%")

    print()
    print("=== Summary ===")
    for agreement, bucket in stats.items():
        total = sum(bucket.values())
        if total == 0:
            continue
        print(f"\n{agreement.upper()} ({total} predictions):")
        for k, v in bucket.items():
            if v:
                print(f"  {k}: {v} ({v/total*100:.0f}%)")


if __name__ == "__main__":
    main()
