#!/usr/bin/env python3
"""
Re-report the backtest over ONLY the trades whose funding cost was measured
rather than imputed.

OKX publishes ~3 months of funding history against years of OHLCV. Trades
outside that window have funding imputed from the median rate (see
backtest.funding_between). This split shows whether the headline result
depends on that imputation.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import backtest as bt

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
raw = json.load(open(os.path.join(REPO, "analysis", "backtest_trades.json")))

LABELS = [("s1", "S1 mean reversion"), ("s2", "S2 OI/funding squeeze"),
          ("s3", "S3 consolidation breakout"),
          ("blended", "BLENDED (flat 0.50% risk)"),
          ("blended_tiered", "BLENDED (tiered sizing)")]

print("FUNDING-EXACT SUB-PERIOD — only trades whose funding cost was measured\n")
print(bt.HEADER)
print("-" * 140)
out = []
for key, label in LABELS:
    trades = raw.get(key, [])
    exact = [t for t in trades if not t.get("funding_imputed")]
    m = bt.metrics(exact, label)
    out.append(m)
    print(bt.fmt(m))
print("-" * 140)

print("\nIMPUTATION EXPOSURE")
for key, label in LABELS:
    trades = raw.get(key, [])
    if not trades:
        continue
    imp = sum(1 for t in trades if t.get("funding_imputed"))
    print(f"  {label:<28} {imp:>5}/{len(trades):<5} trades had imputed funding "
          f"({imp/len(trades)*100:.0f}%)")

json.dump(out, open(os.path.join(REPO, "analysis", "funding_exact_results.json"), "w"),
          indent=2, default=str)
