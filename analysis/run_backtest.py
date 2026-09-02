#!/usr/bin/env python3
"""Driver: fetch the watchlist's history, replay each strategy, report."""

import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import backtest as bt
import hermes_data as hd

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def watchlist():
    """The universe the bot actually evaluated, taken from signal_log.jsonl."""
    syms = set()
    path = os.path.join(REPO, "signal_log.jsonl")
    for line in open(path):
        line = line.strip()
        if line:
            try:
                syms.add(json.loads(line)["symbol"])
            except (json.JSONDecodeError, KeyError):
                pass
    return sorted(syms)


def main():
    wl = watchlist()
    print(f"Watchlist from signal_log.jsonl: {len(wl)} symbols")
    print("Fetching OKX history (4H + 1D + funding + open interest)...\n")

    universe, states, skipped = {}, {}, []
    t0 = time.time()
    for i, sym in enumerate(wl):
        d = bt.load_symbol(sym, max_bars=6000)
        if not d:
            skipped.append(sym)
            continue
        universe[sym] = d
        states[sym] = bt.build_states(d)
        span = (d["c4h"][-1]["ts"] - d["c4h"][0]["ts"]) / 86_400_000
        print(f"  [{i+1:>2}/{len(wl)}] {sym:<13} {len(d['c4h']):>5} 4H bars "
              f"({span:>5.0f}d)  {len(d['c1d']):>4} 1D  "
              f"{len(d['funding']):>4} funding  {len(d['oi_list']):>4} OI  "
              f"-> {len(states[sym])} evaluable bars", flush=True)

    print(f"\nLoaded {len(universe)} symbols in {time.time()-t0:.0f}s; "
          f"{len(skipped)} not available on OKX: {', '.join(skipped) if skipped else '-'}")

    all_bars = [c["ts"] for d in universe.values() for c in d["c4h"]]
    print(f"History span: "
          f"{time.strftime('%Y-%m-%d', time.gmtime(min(all_bars)/1000))} -> "
          f"{time.strftime('%Y-%m-%d', time.gmtime(max(all_bars)/1000))}")

    results, raw = [], {}

    for key, name in (("s1", "S1 mean reversion"),
                      ("s2", "S2 OI/funding squeeze"),
                      ("s3", "S3 consolidation breakout")):
        print(f"\nReplaying {name} independently...", flush=True)
        trades = bt.dedup_trades(bt.run_single_strategy(universe, states, key))
        raw[key] = trades
        results.append(bt.metrics(trades, name))
        print(f"  {len(trades)} episode-deduplicated trades")

    print("\nReplaying blended / conviction-stacked configuration...", flush=True)
    blended = bt.dedup_trades(bt.run_blended(universe, states, tiered_sizing=False))
    raw["blended"] = blended
    results.append(bt.metrics(blended, "BLENDED (flat 0.50% risk)"))
    print(f"  {len(blended)} episode-deduplicated trades")

    print("\nReplaying blended with the new tiered sizing (0.75/0.50/0.35%)...", flush=True)
    tiered = bt.dedup_trades(bt.run_blended(universe, states, tiered_sizing=True))
    raw["blended_tiered"] = tiered
    results.append(bt.metrics(tiered, "BLENDED (tiered sizing)"))

    print("\n" + "=" * 140)
    print("WALK-FORWARD RESULTS — episode-deduplicated, net of fees, funding and slippage")
    print("=" * 140)
    print(bt.HEADER)
    print("-" * 140)
    for m in results:
        print(bt.fmt(m))
    print("-" * 140)

    print("\nCOST DECOMPOSITION (why gross and net differ)")
    print(f"{'Configuration':<26} {'gross exp R':>12} {'net exp R':>11} "
          f"{'fees $':>10} {'funding $':>11} {'slippage $':>11} {'avg MAE R':>10} {'avg MFE R':>10}")
    print("-" * 108)
    for m in results:
        if m["n"] == 0:
            continue
        print(f"{m['label']:<26} {m['gross_expectancy_r']:>+12.4f} {m['expectancy_r']:>+11.4f} "
              f"{m['total_fees']:>10.2f} {m['total_funding']:>11.2f} "
              f"{m['total_slippage']:>11.2f} {m['avg_mae_r']:>10.2f} {m['avg_mfe_r']:>10.2f}")
    print("-" * 108)

    print("\nEXIT REASON MIX")
    for m in results:
        if m["n"]:
            print(f"  {m['label']:<26} {m['exit_reasons']}")

    dest = os.path.join(REPO, "analysis", "backtest_results.json")
    json.dump({"results": results,
               "meta": {"symbols": len(universe), "skipped": skipped,
                        "slippage_bps": bt.SLIPPAGE_BPS,
                        "taker_fee": bt.TAKER_FEE_RATE,
                        "risk_pct_flat": bt.RISK_PCT_FLAT}},
              open(dest, "w"), indent=2, default=str)
    with open(os.path.join(REPO, "analysis", "backtest_trades.json"), "w") as f:
        json.dump({k: v for k, v in raw.items()}, f, default=str)
    print(f"\nWrote {dest}")


if __name__ == "__main__":
    main()
