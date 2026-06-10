#!/usr/bin/env python3
"""
Analyze signal_log.jsonl: for every non-HOLD signal the bot evaluated
(executed or skipped), fetch the current live price and compare it to the
price at evaluation time. This shows whether skipped/blocked signals would
have been profitable (missed opportunity) or would have lost money (gate
working as intended).

Usage:
    python3 analyze_signals.py
"""
import json
import time
import requests

LOG_PATH = "signal_log.jsonl"


def fetch_price(symbol):
    resp = requests.get(
        f"https://api.bybit.com/v5/market/tickers?category=linear&symbol={symbol}",
        timeout=10,
    )
    data = resp.json()
    if data.get("retCode") != 0:
        return None
    return float(data["result"]["list"][0]["lastPrice"])


def would_have_won(action, change_pct):
    if action == "BUY":
        return change_pct > 0
    if action == "SELL":
        return change_pct < 0
    return None


def main():
    try:
        with open(LOG_PATH) as f:
            lines = [json.loads(l) for l in f if l.strip()]
    except FileNotFoundError:
        print(f"{LOG_PATH} not found — no signals logged yet.")
        return

    if not lines:
        print("No entries yet.")
        return

    price_cache = {}

    # skip_reason -> {"won": n, "lost": n, "pnl_pct": sum}
    skip_stats = {}
    executed_stats = {"won": 0, "lost": 0, "pnl_pct": 0.0}

    print(f"{'Symbol':<12} {'Action':<5} {'Conv':<4} {'Conf':>5} {'Executed':<9} "
          f"{'EntryPrice':>12} {'NowPrice':>12} {'Chg%':>7} {'Result':<6} {'SkipReason'}")
    print("-" * 110)

    for entry in lines:
        symbol = entry["symbol"]
        if symbol not in price_cache:
            price_cache[symbol] = fetch_price(symbol)
            time.sleep(0.1)  # be polite to the API
        now_price = price_cache[symbol]
        entry_price = entry.get("price")
        if not now_price or not entry_price:
            continue

        action = entry.get("action")
        change_pct = (now_price - entry_price) / entry_price * 100.0
        won = would_have_won(action, change_pct)
        result = "WIN" if won else "LOSS" if won is False else "?"

        executed = entry.get("executed", False)
        skip_reason = entry.get("skip_reason", "")

        if executed:
            if won:
                executed_stats["won"] += 1
                executed_stats["pnl_pct"] += abs(change_pct)
            elif won is False:
                executed_stats["lost"] += 1
                executed_stats["pnl_pct"] -= abs(change_pct)
        else:
            bucket = skip_stats.setdefault(skip_reason, {"won": 0, "lost": 0, "pnl_pct": 0.0})
            if won:
                bucket["won"] += 1
                bucket["pnl_pct"] += abs(change_pct)
            elif won is False:
                bucket["lost"] += 1
                bucket["pnl_pct"] -= abs(change_pct)

        print(f"{symbol:<12} {action:<5} {entry.get('conviction'):<4} "
              f"{entry.get('confidence', 0)*100:>4.0f}% {str(executed):<9} "
              f"{entry_price:>12.4f} {now_price:>12.4f} {change_pct:>6.2f}% {result:<6} {skip_reason}")

    print()
    print("=== Summary ===")
    print("\nEXECUTED trades:")
    total = executed_stats["won"] + executed_stats["lost"]
    if total:
        print(f"  Won: {executed_stats['won']}/{total} "
              f"({executed_stats['won']/total*100:.0f}%) — net |move| sum: {executed_stats['pnl_pct']:+.2f}%")
    else:
        print("  (none yet)")

    print("\nSKIPPED signals (would they have made money?):")
    for reason, bucket in skip_stats.items():
        total = bucket["won"] + bucket["lost"]
        if total == 0:
            continue
        verdict = "MISSED PROFIT" if bucket["pnl_pct"] > 0 else "AVOIDED LOSS"
        print(f"  [{reason}]")
        print(f"    Would-be win rate: {bucket['won']}/{total} ({bucket['won']/total*100:.0f}%) — "
              f"net |move| sum: {bucket['pnl_pct']:+.2f}% -> {verdict}")


if __name__ == "__main__":
    main()
