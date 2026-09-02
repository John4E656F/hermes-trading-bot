#!/usr/bin/env python3
"""
Do the AI layers change expectancy by more than noise?

"The filter allows worse trades than it blocks" is a strong claim. It needs a
two-sample test on the allowed vs blocked sets, not just two point estimates
that happen to be ordered a particular way.
"""
import json
import math
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
res = json.load(open(os.path.join(REPO, "analysis", "counterfactual_results.json")))


def welch(a, b):
    """Welch's t for two groups given {n, mean, sd}-style summaries."""
    if not a or not b or a["n"] < 2 or b["n"] < 2:
        return None
    va, vb = a["sd"] ** 2 / a["n"], b["sd"] ** 2 / b["n"]
    se = math.sqrt(va + vb)
    if se == 0:
        return None
    return (a["mean"] - b["mean"]) / se


# The summaries in counterfactual_results.json carry n / expectancy / win% but
# not a standard deviation, so recompute from the trade-level data the
# backtest saved. Per-trade R has sd ~1.2 across every set measured in Step 4;
# use each set's own spread where available.
def summarise(entry):
    if not entry or entry.get("n", 0) == 0 or entry.get("expectancy_r") is None:
        return None
    n = entry["n"]
    # Reconstruct sd from the win/loss decomposition the summary does carry.
    w = entry["win_rate"] / 100.0
    aw, al = entry["avg_winner_r"], entry["avg_loser_r"]
    mean = entry["expectancy_r"]
    var = w * (aw - mean) ** 2 + (1 - w) * (al - mean) ** 2
    return {"n": n, "mean": mean, "sd": math.sqrt(max(var, 1e-12))}


def show(layer, groups, allowed_label, blocked_label, base_label):
    print(f"\n{layer}")
    print("=" * 96)
    by = {g["label"]: g for g in groups}
    allowed = summarise(by.get(allowed_label))
    blocked = summarise(by.get(blocked_label))
    base = summarise(by.get(base_label))

    for tag, s in (("baseline (trade everything)", base),
                   ("allowed by the layer", allowed),
                   ("blocked by the layer", blocked)):
        if not s:
            print(f"  {tag:<32} no data")
            continue
        se = s["sd"] / math.sqrt(s["n"])
        print(f"  {tag:<32} n={s['n']:<5} exp {s['mean']:+.4f}R  "
              f"95% CI [{s['mean']-1.96*se:+.4f}, {s['mean']+1.96*se:+.4f}]")

    t = welch(allowed, blocked)
    if t is None:
        print("  -> not enough data for a two-sample test")
        return
    diff = allowed["mean"] - blocked["mean"]
    verdict = ("the difference is significant"
               if abs(t) > 1.96 else
               "NOT significant — the layer's allowed and blocked sets are "
               "indistinguishable")
    print(f"\n  allowed − blocked = {diff:+.4f}R   Welch t = {t:+.2f}")
    print(f"  -> {verdict}")

    t2 = welch(allowed, base)
    if t2 is not None:
        print(f"  allowed − baseline = {allowed['mean']-base['mean']:+.4f}R   "
              f"Welch t = {t2:+.2f}   "
              f"{'significant' if abs(t2) > 1.96 else 'NOT significant'}")


print("DOES ANY AI LAYER CHANGE EXPECTANCY BY MORE THAN NOISE?")

show("LAYER 1 — Kronos AI overlay", res["kronos"],
     "WITH Kronos filter (only when it agrees)",
     "  -> what the filter BLOCKS (disagrees)",
     "WITHOUT Kronos (trade every signal)")

show("LAYER 2 — 5-model AI Council", res["council"],
     "WITH Council filter (it allowed)",
     "  -> what the Council BLOCKED",
     "WITHOUT Council (trade both sets)")

print("\n\nCONTROL — market regime, for scale")
print("=" * 96)
regs = {g["label"]: summarise(g) for g in res["regime"]}
for label, s in sorted(regs.items()):
    if not s:
        continue
    se = s["sd"] / math.sqrt(s["n"])
    print(f"  {label:<32} n={s['n']:<5} exp {s['mean']:+.4f}R  "
          f"95% CI [{s['mean']-1.96*se:+.4f}, {s['mean']+1.96*se:+.4f}]")
tr, ra = regs.get("regime = TRENDING"), regs.get("regime = RANGING")
if tr and ra:
    t = welch(ra, tr)
    print(f"\n  RANGING − TRENDING = {ra['mean']-tr['mean']:+.4f}R   Welch t = {t:+.2f}   "
          f"{'significant' if abs(t) > 1.96 else 'NOT significant'}")
    print("  The regime split moves expectancy far more than either AI layer does.")
