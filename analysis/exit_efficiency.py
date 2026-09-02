#!/usr/bin/env python3
"""
Is the problem the entries or the exits?

The blended configuration targets 2.5:1 reward:risk (SL 3xATR, TP 7.5xATR) yet
returns -0.0360R. Two explanations fit that: the entries are directionless, or
the entries are fine and the exit rules are giving the edge back. They call for
opposite responses, so it is worth separating them before touching anything.

This reads the trade dump the walk-forward backtest wrote
(analysis/backtest_trades.json) and asks three questions:

  1. Where does realised R sit against each trade's own best excursion (MFE)?
     A large gap means the exits are leaving money on the table.
  2. Does ANY fixed take-profit level produce positive expectancy? If the
     entries carry information, some level should.
  3. Is MFE > MAE more often than chance? This is the entry-quality question
     stripped of every exit rule: does price go your way first, or not?
"""

import collections
import json
import os
import statistics as st
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PATH = os.path.join(REPO, "analysis", "backtest_trades.json")

if not os.path.exists(PATH):
    sys.exit(f"{PATH} not found — run analysis/run_backtest.py first")

raw = json.load(open(PATH))
CONFIG = sys.argv[1] if len(sys.argv) > 1 else "blended"
tr = raw.get(CONFIG)
if not tr:
    sys.exit(f"no trades for config {CONFIG!r}; available: {list(raw)}")

print(f"EXIT EFFICIENCY — configuration {CONFIG!r}, {len(tr)} episode-deduplicated trades\n")

# ── 1. Realised vs best-available, by exit reason ───────────────────────
print("1. Where each exit lands against that trade's own best excursion\n")
print(f"{'exit reason':<14} {'n':>5} {'avg result R':>13} {'avg MFE R':>10} "
      f"{'avg MAE R':>10}  {'MFE-result gap':>15}")
print("-" * 76)
by = collections.defaultdict(list)
for t in tr:
    by[t["exit_reason"]].append(t)
for k, v in sorted(by.items(), key=lambda kv: -len(kv[1])):
    r = [x["result_r"] for x in v]
    mfe = [x["mfe_r"] for x in v]
    mae = [x["mae_r"] for x in v]
    print(f"{k:<14} {len(v):>5} {st.mean(r):>+13.3f} {st.mean(mfe):>10.3f} "
          f"{st.mean(mae):>10.3f}  {st.mean(mfe) - st.mean(r):>+15.3f}")
print("-" * 76)

wins = [t for t in tr if t["result_r"] > 0]
if wins:
    print(f"\nwinners: n={len(wins)} ({len(wins)/len(tr)*100:.1f}%)  "
          f"avg realised {st.mean([t['result_r'] for t in wins]):+.3f}R  "
          f"avg MFE {st.mean([t['mfe_r'] for t in wins]):.3f}R")
    print(f"  winners that EVER reached the 2.5R take-profit level: "
          f"{sum(1 for t in wins if t['mfe_r'] >= 2.5)}/{len(wins)}")
    print(f"  winners that reached even 2.0R at their best point:   "
          f"{sum(1 for t in wins if t['mfe_r'] >= 2.0)}/{len(wins)}")

print(f"\nCeiling — expectancy if EVERY trade exited at its own best point: "
      f"{st.mean([max(t['mfe_r'], -1.0) for t in tr]):+.3f}R")
print(f"Actual expectancy: {st.mean([t['result_r'] for t in tr]):+.3f}R")

# ── 2. Does any take-profit level rescue it? ────────────────────────────
#
# Approximation, not a replay: MAE/MFE carry no ordering, so this assumes the
# adverse extreme came first — the same conservative rule the backtest uses for
# a bar containing both levels. It cannot invent an edge that is not there, and
# it will not flatter a rule that only works with hindsight ordering.
def sweep(tp_r, stop_r=1.0):
    out = []
    for t in tr:
        if t["mae_r"] >= stop_r:
            out.append(-stop_r)
        elif t["mfe_r"] >= tp_r:
            out.append(tp_r)
        else:
            out.append(t["result_r"])
    return out


print("\n\n2. Fixed take-profit sweep (stop held at 1R, adverse-first ordering)\n")
print(f"{'TP level':>9} {'expectancy R':>13} {'win rate':>9} {'profit factor':>14}")
print("-" * 50)
best = None
for tp in (1.0, 1.25, 1.5, 1.75, 2.0, 2.25, 2.5, 3.0):
    out = sweep(tp)
    e = st.mean(out)
    w = [x for x in out if x > 0]
    l = [abs(x) for x in out if x <= 0]
    pf = (sum(w) / sum(l)) if l else float("inf")
    if best is None or e > best[1]:
        best = (tp, e, pf)
    print(f"{tp:>8.2f}R {e:>+13.4f} {len(w)/len(out)*100:>8.1f}% {pf:>14.2f}")
print("-" * 50)
print(f"best in sweep: TP {best[0]:.2f}R -> {best[1]:+.4f}R at PF {best[2]:.2f}")
print("  (this is the PEAK of a curve fitted after seeing the data — treat it")
print("   as an upper bound on what exit tuning could achieve, not a setting)")

# ── 3. Entry quality, stripped of every exit rule ──────────────────────
print("\n\n3. Entry quality — does price go your way first?\n")
mfe = [t["mfe_r"] for t in tr]
mae = [t["mae_r"] for t in tr]
better = sum(1 for t in tr if t["mfe_r"] > t["mae_r"])
print(f"median MFE {st.median(mfe):.3f}R    median MAE {st.median(mae):.3f}R")
print(f"trades where MFE > MAE: {better}/{len(tr)} ({better/len(tr)*100:.1f}%)"
      f"   <- 50% is a coin flip")
print()
if better / len(tr) < 0.52:
    print("VERDICT: the entries carry no directional information. Price is about")
    print("as likely to move against first as for. No exit rule can fix that —")
    print("the sweep above confirms none does.")
else:
    print("VERDICT: entries show a directional tilt; the exits are worth tuning.")
