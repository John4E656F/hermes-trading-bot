#!/usr/bin/env python3
"""Assert the O(n) series forms equal the O(n^2) pointwise forms everywhere."""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import hermes_data as hd
import indicators as ind

c = hd.fetch_candles("BTCUSDT", "1H", max_bars=3000)[:600]
print(f"Verifying series forms against pointwise forms over {len(c)} candles\n")

checks = [
    ("ema20", ind.ema_series(c, 20), lambda i: ind.ema(c[:i + 1], 20), 19),
    ("rsi14", ind.rsi_series(c, 14), lambda i: ind.rsi(c[:i + 1], 14), 14),
    ("atr14", ind.atr_series(c, 14), lambda i: ind.atr(c[:i + 1], 14), 14),
    ("adx14", ind.adx_series(c, 14), lambda i: ind.adx(c[:i + 1], 14), 27),
]

ok = True
for name, series, point, start in checks:
    worst, worst_i = 0.0, -1
    n = 0
    for i in range(start, len(c)):
        s, p = series[i], point(i)
        if s is None:
            continue
        n += 1
        rel = abs(s - p) / abs(p) if p else abs(s - p)
        if rel > worst:
            worst, worst_i = rel, i
    good = worst < 1e-9
    ok &= good
    print(f"  {name:<8} {n:>4} indices compared   max rel.diff {worst:.3e} "
          f"(at i={worst_i})   {'MATCH' if good else '*** MISMATCH ***'}")

print("\n" + ("All series forms are exact." if ok else "MISMATCH — do not trust the backtest."))
sys.exit(0 if ok else 1)
