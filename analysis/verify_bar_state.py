#!/usr/bin/env python3
"""
Assert the fast build_states path produces the SAME bar state as recomputing
from the full prefix the way the Go code does.

The fast path passes only a 60-bar tail plus precomputed recursive values. If
any windowed indicator secretly needed more history, this would catch it.
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import backtest as bt
import indicators as ind
import strategy as sg

d = bt.load_symbol("BTCUSDT", max_bars=1200)
states = bt.build_states(d)
c4h, c1d = d["c4h"], d["c1d"]

idxs = sorted(states)[::37][:40]
print(f"Comparing {len(idxs)} bars: fast path vs full-prefix recomputation\n")

FIELDS = ["price", "ema20", "sma50", "rsi14", "wr", "vwap20", "atr14",
          "bb_upper", "bb_basis", "bb_lower", "bb_width", "avg_vol",
          "latest_vol", "vol_ratio", "daily_adx", "gain7d"]

worst = {f: 0.0 for f in FIELDS}
sig_mismatch = 0

for i in idxs:
    ts = c4h[i]["ts"]
    di = 0
    while di < len(c1d) and c1d[di]["ts"] + 86_400_000 <= ts:
        di += 1
    fr = bt.funding_at(d["funding"], ts) if d["funding"] else None
    oi_cur = d["oi"].get(ts)
    oi_chg = 0.0
    if oi_cur:
        prev = d["oi"].get(ts - 6 * bt.BAR_MS)
        if prev and prev > 0:
            oi_chg = (oi_cur - prev) / prev * 100.0
        else:
            oi_cur = None

    slow = sg.compute_bar_state(c4h[:i + 1], c1d[:di], fr, oi_chg, oi_cur)
    fast = states[i]

    for f in FIELDS:
        a, b = fast[f], slow[f]
        if a is None or b is None:
            continue
        rel = abs(a - b) / abs(b) if b else abs(a - b)
        worst[f] = max(worst[f], rel)

    for lens in ("s1", "s2", "s3", "s4", "s5"):
        if fast[lens] != slow[lens]:
            sig_mismatch += 1
    if sg.master_signal(fast) != sg.master_signal(slow):
        sig_mismatch += 1

ok = all(v < 1e-9 for v in worst.values()) and sig_mismatch == 0
for f in FIELDS:
    print(f"  {f:<12} max rel.diff {worst[f]:.3e}  {'ok' if worst[f] < 1e-9 else '*** MISMATCH ***'}")
print(f"\n  sub-strategy + master-signal disagreements: {sig_mismatch}")
print("\n" + ("Fast path is equivalent to full-prefix recomputation."
             if ok else "MISMATCH — the fast path changes the signals."))
sys.exit(0 if ok else 1)
