#!/usr/bin/env python3
"""
Is any of this distinguishable from zero?

An expectancy of -0.036R over 544 trades and an expectancy of +0.25R over 95
are not comparable claims until you attach an error bar to each. This computes
a two-sided t-statistic on per-trade R and a 95% confidence interval, and
checks whether the "funding-exact" split is a funding effect or simply a
different (more recent) slice of time.
"""
import datetime as dt
import json
import math
import os
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
raw = json.load(open(os.path.join(REPO, "analysis", "backtest_trades.json")))

LABELS = [("s1", "S1 mean reversion"), ("s2", "S2 OI/funding squeeze"),
          ("s3", "S3 consolidation breakout"),
          ("blended", "BLENDED"), ("blended_tiered", "BLENDED (tiered)")]


def stats(trades):
    rs = [t["result_r"] for t in trades]
    n = len(rs)
    if n < 2:
        return None
    mean = sum(rs) / n
    sd = math.sqrt(sum((r - mean) ** 2 for r in rs) / (n - 1))
    se = sd / math.sqrt(n)
    t = mean / se if se > 0 else 0.0
    return {"n": n, "mean": mean, "sd": sd, "se": se, "t": t,
            "lo": mean - 1.96 * se, "hi": mean + 1.96 * se}


def day(ms):
    return dt.datetime.utcfromtimestamp(ms / 1000).strftime("%Y-%m-%d")


print("EXPECTANCY WITH ERROR BARS (per-trade R, two-sided t)\n")
print(f"{'Configuration':<28} {'n':>6} {'exp R':>9} {'sd':>7} {'95% CI':>20} "
      f"{'t':>7}  significant?")
print("-" * 92)
for key, label in LABELS:
    s = stats(raw.get(key, []))
    if not s:
        print(f"{label:<28} {len(raw.get(key, [])):>6}   too few trades to test")
        continue
    sig = "yes" if abs(s["t"]) > 1.96 else "NO — indistinguishable from zero"
    ci = f"[{s['lo']:+.4f}, {s['hi']:+.4f}]"
    print(f"{label:<28} {s['n']:>6} {s['mean']:>+9.4f} {s['sd']:>7.2f} {ci:>20} "
          f"{s['t']:>+7.2f}  {sig}")
print("-" * 92)

print("\n\nIS THE 'FUNDING-EXACT' RESULT A FUNDING EFFECT, OR JUST A RECENT PERIOD?\n")
print(f"{'Configuration':<28} {'set':<16} {'n':>5} {'exp R':>9} {'first entry':>12} "
      f"{'last exit':>12}")
print("-" * 92)
for key, label in LABELS[:4]:
    trades = raw.get(key, [])
    if not trades:
        continue
    exact = [t for t in trades if not t.get("funding_imputed")]
    imputed = [t for t in trades if t.get("funding_imputed")]
    for name, subset in (("funding-exact", exact), ("funding-imputed", imputed)):
        if not subset:
            continue
        s = stats(subset)
        print(f"{label:<28} {name:<16} {len(subset):>5} "
              f"{(s['mean'] if s else float('nan')):>+9.4f} "
              f"{day(min(t['entry_ts'] for t in subset)):>12} "
              f"{day(max(t['exit_ts'] for t in subset)):>12}")
print("-" * 92)

# The decisive check: within the funding-exact window, do the two groups
# actually overlap in time, or is 'exact' simply 'recent'?
tr = raw.get("blended", [])
exact = [t for t in tr if not t.get("funding_imputed")]
imputed = [t for t in tr if t.get("funding_imputed")]
if exact and imputed:
    ex_start = min(t["entry_ts"] for t in exact)
    im_after = [t for t in imputed if t["entry_ts"] >= ex_start]
    print(f"\nBLENDED: funding-exact trades start {day(ex_start)}.")
    print(f"  Imputed trades entered on or after that date: {len(im_after)}")
    if not im_after:
        print("  -> The two groups do not overlap in time at all.")
        print("     'Funding-exact' is a synonym for 'the last three months'.")
        print("     The split therefore says NOTHING about whether imputation")
        print("     distorted the result; it is a period comparison.")

    # Same-period comparison for the full sample, to size the recency effect.
    recent = [t for t in tr if t["entry_ts"] >= ex_start]
    older = [t for t in tr if t["entry_ts"] < ex_start]
    for name, subset in (("last 3 months", recent), ("everything before", older)):
        s = stats(subset)
        if s:
            print(f"  {name:<20} n={s['n']:<5} exp {s['mean']:+.4f}R  "
                  f"95% CI [{s['lo']:+.4f}, {s['hi']:+.4f}]  t={s['t']:+.2f}")
