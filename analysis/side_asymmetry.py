#!/usr/bin/env python3
"""
Re-derive the BUY/SELL asymmetry that motivated the side-aware sizing change
in commit 0948c60, on the episode-deduplicated sample and with the short-side
sign convention the bot itself uses.

The change cites "2,971 calls" as its evidence base. That is exactly the raw
graded master-action count in kronos_outcomes.jsonl, which the episode
deduplication in analysis/episode_dedup.py shows to be inflated ~9x: the same
standing directional call is re-logged every cron cycle and each row is then
counted as an independent observation.

Two separate problems compound here, so this script checks both:

  1. SAMPLE INFLATION. Deduplicating to independent episodes changes which
     side looks broken.
  2. SIGN CONVENTION. A short profits when price FALLS, so its PnL is
     -change_pct. Summing raw change_pct over SELL rows measures the opposite
     of what a short earned. The bot's own master_result labels are used below
     to verify which convention is correct.
"""

import collections
import json
import os
import statistics as st
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from episode_dedup import load_jsonl, parse_ts, dedup_runs

rows = load_jsonl("kronos_outcomes.jsonl")
if not rows:
    sys.exit("kronos_outcomes.jsonl not found")
for r in rows:
    r["_ts"] = parse_ts(r.get("timestamp"))

GRADED = ("correct", "incorrect")


# ── 0. Which sign convention does the bot itself use? ───────────────────
print("0. Sign convention, checked against the bot's own result labels\n")
for side, fell_is_correct in (("SELL", True), ("BUY", False)):
    sub = [r for r in rows if r.get("master_action") == side and r.get("master_result") in GRADED]
    agree = sum(1 for r in sub
                if ((r["change_pct"] < 0) == (r["master_result"] == "correct")) == fell_is_correct)
    print(f"   {side:<5} rows where the label matches "
          f"'{'price fell' if fell_is_correct else 'price rose'} == correct': {agree}/{len(sub)}")
print("\n   -> a SELL is scored CORRECT when price FELL, so short PnL = -change_pct.")
print("      Summing raw change_pct over SELL rows measures the INVERSE of short PnL.\n")


def summarise(rs, label):
    print(f"\n{label}")
    print(f"{'side':<6}{'n':>6}{'WR':>8}{'avg win%':>10}{'avg loss%':>11}"
          f"{'W/L ratio':>11}{'sum PnL%':>11}")
    out = {}
    for side in ("BUY", "SELL"):
        sub = [r for r in rs if r.get("master_action") == side and r.get("master_result") in GRADED]
        if not sub:
            continue
        # Directional PnL: long earns +change_pct, short earns -change_pct.
        moves = [(r["change_pct"] if side == "BUY" else -r["change_pct"]) for r in sub]
        wins = [m for m in moves if m > 0]
        losses = [abs(m) for m in moves if m <= 0]
        ratio = (st.mean(wins) / st.mean(losses)) if wins and losses else float("nan")
        out[side] = dict(n=len(sub), wr=len(wins) / len(sub) * 100,
                         avgw=st.mean(wins) if wins else 0.0,
                         avgl=st.mean(losses) if losses else 0.0,
                         ratio=ratio, total=sum(moves))
        v = out[side]
        print(f"{side:<6}{v['n']:>6}{v['wr']:>7.1f}%{v['avgw']:>10.2f}"
              f"{v['avgl']:>11.2f}{v['ratio']:>11.2f}{v['total']:>+11.1f}")
    return out


raw = summarise(rows, "RAW — every logged row counted as an independent call")
episodes = dedup_runs(rows,
                      group_fn=lambda r: (r.get("symbol"), r.get("master_action")),
                      time_fn=lambda r: r.get("_ts"))
ded = summarise(episodes, "EPISODE-DEDUPLICATED — one row per independent directional call")

print(f"\ninflation: {len(rows)} rows -> {len(episodes)} episodes "
      f"({len(rows)/len(episodes):.2f}x)")

if "BUY" in raw and "BUY" in ded:
    print("\n\nWIN/LOSS SIZE RATIO — the statistic the sizing change was built on")
    print(f"{'':6}{'raw':>10}{'deduplicated':>16}")
    print(f"{'BUY':<6}{raw['BUY']['ratio']:>10.2f}{ded['BUY']['ratio']:>16.2f}")
    print(f"{'SELL':<6}{raw['SELL']['ratio']:>10.2f}{ded['SELL']['ratio']:>16.2f}")
    print()
    worse_raw = "BUY" if raw["BUY"]["ratio"] < raw["SELL"]["ratio"] else "SELL"
    worse_ded = "BUY" if ded["BUY"]["ratio"] < ded["SELL"]["ratio"] else "SELL"
    print(f"   raw sample says the broken side is:          {worse_raw}")
    print(f"   deduplicated sample says it is:              {worse_ded}")
    if worse_raw != worse_ded:
        print("\n   THESE DISAGREE. The conclusion reverses once the same standing")
        print("   call stops being counted ~9 times. Sizing down the side the raw")
        print("   sample blames would penalise the better of the two.")
